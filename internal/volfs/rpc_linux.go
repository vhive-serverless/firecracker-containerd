// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may
// not use this file except in compliance with the License. A copy of the
// License is located at
//
//	http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.

package volfs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// ProtocolVersion identifies the descriptor RPC schema and replay semantics.
	ProtocolVersion = 1
	// MaxFrame bounds a JSON frame, including base64-encoded IO and names.
	MaxFrame = 2 << 20
	// VolumePort is the guest-to-host vsock port reserved for Pod volumes.
	VolumePort     uint32 = 10901
	maxExports            = 256
	maxConnections        = 64
	rpcTimeout            = 5 * time.Second
	opGetattr             = "getattr"
	opUnlink              = "unlink"
	opCreate              = "create"
	opRelease             = "release"
)

// Descriptor authorizes access to one export without disclosing a host path.
// The token is a capability and must only be delivered to the owning VM.
type Descriptor struct {
	Token    string
	FileOnly bool
	ReadOnly bool
}

// Request contains exactly one filesystem operation. Names and link targets are
// byte sequences so JSON transport preserves names that are not valid UTF-8.
type Request struct {
	Version                                uint32
	Token, Session                         string
	ID                                     uint64
	Operation                              string
	Node, Parent, NewParent, Handle, Count uint64
	Name, NewName, Target, Data            []byte
	Offset                                 int64
	Size                                   int
	Flags, Mode                            uint32
	Cred                                   Credentials
	Change                                 *SetAttr
	Directory, DataOnly                    bool
}

// Reply records both successful results and filesystem errno values. A fatal
// reply means the client cannot continue the session's ordered request stream.
type Reply struct {
	ID      uint64
	Status  uint32
	Fatal   bool
	Entry   *Entry         `json:",omitempty"`
	Attr    *Attr          `json:",omitempty"`
	Entries []Dirent       `json:",omitempty"`
	Data    []byte         `json:",omitempty"`
	Handle  uint64         `json:",omitempty"`
	Written int            `json:",omitempty"`
	Statfs  *unix.Statfs_t `json:",omitempty"`
}

type servedExport struct {
	mu        sync.Mutex
	export    *Export
	closed    bool
	session   string
	lastID    uint64
	lastHash  [sha256.Size]byte
	lastReply []byte
}

// Server owns registered exports and their replay records. One guest mount
// owns each export's session; reconnecting that session preserves its handles.
type Server struct {
	checkpoint  sync.RWMutex
	mu          sync.Mutex
	closed      bool
	listener    net.Listener
	exports     map[string]*servedExport
	connections map[net.Conn]struct{}
	workers     sync.WaitGroup
	closeOnce   sync.Once
	closeErr    error
}

// NewServer creates a bounded registry; Serve supplies its VM-scoped listener.
func NewServer() *Server {
	return &Server{exports: make(map[string]*servedExport), connections: make(map[net.Conn]struct{})}
}

func randomToken() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}

func validToken(token string) bool {
	if len(token) != 32 {
		return false
	}
	for _, ch := range token {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

// Register transfers ownership on success. Callers close an export themselves
// if registration fails. Unregister or Close releases successful registrations.
func (s *Server) Register(export *Export) (Descriptor, error) {
	s.checkpoint.RLock()
	defer s.checkpoint.RUnlock()
	if export == nil {
		return Descriptor{}, unix.EINVAL
	}
	token, err := randomToken()
	if err != nil {
		return Descriptor{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Descriptor{}, unix.EBADF
	}
	if len(s.exports) >= maxExports {
		return Descriptor{}, unix.ENFILE
	}
	if s.exports[token] != nil {
		return Descriptor{}, unix.EEXIST
	}
	s.exports[token] = &servedExport{export: export}
	return Descriptor{Token: token, FileOnly: export.fileOnly, ReadOnly: export.readOnly}, nil
}

func (e *servedExport) close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true
	e.lastReply = nil
	return e.export.Close()
}

// Unregister revokes a capability after any active host operation completes.
// Repeated cleanup is safe, including after the whole server has closed.
func (s *Server) Unregister(token string) error {
	s.checkpoint.RLock()
	defer s.checkpoint.RUnlock()
	s.mu.Lock()
	export := s.exports[token]
	delete(s.exports, token)
	s.mu.Unlock()
	if export == nil {
		return nil
	}
	return export.close()
}

// Serve accepts bounded, deadline-protected connections until cancellation or
// Close. The listener must be scoped to the VM authorized for these exports.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	s.mu.Lock()
	if s.closed || s.listener != nil {
		s.mu.Unlock()
		_ = listener.Close()
		return unix.EBUSY
	}
	s.listener = listener
	s.mu.Unlock()
	stop := context.AfterFunc(ctx, func() { _ = s.Close() })
	defer stop()
	defer s.Close()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return nil
			}
			return err
		}
		s.mu.Lock()
		if s.closed || len(s.connections) >= maxConnections {
			s.mu.Unlock()
			_ = conn.Close()
			continue
		}
		s.connections[conn] = struct{}{}
		s.workers.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.workers.Done()
			defer func() {
				_ = conn.Close()
				s.mu.Lock()
				delete(s.connections, conn)
				s.mu.Unlock()
			}()
			s.serveConn(conn)
		}()
	}
}

// Close closes transports, drains active host calls and releases all exports.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		s.checkpoint.Lock()
		s.mu.Lock()
		s.closed = true
		if s.listener != nil {
			_ = s.listener.Close()
		}
		for conn := range s.connections {
			_ = conn.Close()
		}
		exports := s.exports
		s.exports = nil
		s.mu.Unlock()
		for _, export := range exports {
			s.closeErr = errors.Join(s.closeErr, export.close())
		}
		s.checkpoint.Unlock()
		s.workers.Wait()
	})
	return s.closeErr
}

func readFrame(reader io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > MaxFrame {
		return nil, unix.E2BIG
	}
	frame := make([]byte, int(size))
	_, err := io.ReadFull(reader, frame)
	return frame, err
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func writeFrame(writer io.Writer, frame []byte) error {
	if len(frame) == 0 || len(frame) > MaxFrame {
		return unix.E2BIG
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(frame)))
	if err := writeAll(writer, header[:]); err != nil {
		return err
	}
	return writeAll(writer, frame)
}

func fatalReply(id uint64, errno unix.Errno) []byte {
	data, _ := json.Marshal(Reply{ID: id, Status: uint32(errno), Fatal: true})
	return data
}

func (s *Server) handle(frame []byte) []byte {
	s.checkpoint.RLock()
	defer s.checkpoint.RUnlock()
	var request Request
	if err := json.Unmarshal(frame, &request); err != nil {
		return fatalReply(0, unix.EPROTO)
	}
	if request.Version != ProtocolVersion || request.ID == 0 || !validToken(request.Session) || !validToken(request.Token) {
		return fatalReply(request.ID, unix.EPROTO)
	}
	s.mu.Lock()
	export := s.exports[request.Token]
	s.mu.Unlock()
	if export == nil {
		return fatalReply(request.ID, unix.EACCES)
	}
	export.mu.Lock()
	defer export.mu.Unlock()
	if export.closed {
		return fatalReply(request.ID, unix.ESTALE)
	}
	if export.session == "" {
		if request.ID != 1 {
			return fatalReply(request.ID, unix.ESTALE)
		}
		export.session = request.Session
	}
	if request.Session != export.session {
		return fatalReply(request.ID, unix.EBUSY)
	}
	hash := sha256.Sum256(frame)
	if request.ID == export.lastID {
		if hash != export.lastHash {
			return fatalReply(request.ID, unix.EPROTO)
		}
		return export.lastReply
	}
	if request.ID != export.lastID+1 {
		return fatalReply(request.ID, unix.ESTALE)
	}
	reply, err := dispatch(export.export, &request)
	reply.ID = request.ID
	if err != nil {
		var errno unix.Errno
		if !errors.As(err, &errno) {
			errno = unix.EIO
		}
		reply.Status = uint32(errno)
	}
	data, err := json.Marshal(reply)
	if err != nil || len(data) > MaxFrame {
		data, _ = json.Marshal(Reply{ID: request.ID, Status: uint32(unix.E2BIG)})
	}
	// Cache before attempting delivery. Retrying this ID never repeats an IO
	// operation, including when its first reply was lost after a successful write.
	export.lastID, export.lastHash, export.lastReply = request.ID, hash, data
	return data
}

func (s *Server) serveConn(conn net.Conn) {
	for {
		if err := conn.SetDeadline(time.Now().Add(rpcTimeout)); err != nil {
			return
		}
		frame, err := readFrame(conn)
		if err != nil {
			return
		}
		reply := s.handle(frame)
		if err := conn.SetDeadline(time.Now().Add(rpcTimeout)); err != nil {
			return
		}
		if err := writeFrame(conn, reply); err != nil {
			return
		}
	}
}

func dispatch(export *Export, request *Request) (Reply, error) {
	var reply Reply
	var err error
	switch request.Operation {
	case "lookup":
		entry, opErr := export.Lookup(request.Parent, request.Name)
		err = opErr
		if err == nil {
			reply.Entry = &entry
		}
	case opGetattr:
		attr, opErr := export.Getattr(request.Node)
		err = opErr
		if err == nil {
			reply.Attr = &attr
		}
	case "readlink":
		reply.Data, err = export.Readlink(request.Node)
	case "open":
		reply.Handle, err = export.Open(request.Node, int(request.Flags))
	case opCreate:
		entry, handle, opErr := export.Create(request.Parent, request.Name, int(request.Flags), request.Mode, request.Cred)
		err = opErr
		if err == nil {
			reply.Entry, reply.Handle = &entry, handle
		}
	case "mkdir":
		entry, opErr := export.Mkdir(request.Parent, request.Name, request.Mode, request.Cred)
		err = opErr
		if err == nil {
			reply.Entry = &entry
		}
	case "symlink":
		entry, opErr := export.Symlink(request.Parent, request.Name, request.Target, request.Cred)
		err = opErr
		if err == nil {
			reply.Entry = &entry
		}
	case "link":
		entry, opErr := export.Link(request.Node, request.Parent, request.Name)
		err = opErr
		if err == nil {
			reply.Entry = &entry
		}
	case "readdir":
		reply.Entries, err = export.ReadDir(request.Node)
	case "read":
		reply.Data, err = export.Read(request.Handle, request.Offset, request.Size)
	case "write":
		reply.Written, err = export.Write(request.Handle, request.Offset, request.Data)
	case "sync":
		err = export.Sync(request.Handle, request.DataOnly)
	case "syncdir":
		err = export.SyncDir(request.Node)
	case opRelease:
		err = export.Release(request.Handle)
	case "forget":
		export.Forget(request.Node, request.Count)
	case "rename":
		err = export.Rename(request.Parent, request.Name, request.NewParent, request.NewName, request.Flags)
	case opUnlink:
		err = export.Unlink(request.Parent, request.Name, request.Directory)
	case "setattr":
		if request.Change == nil {
			return reply, unix.EINVAL
		}
		attr, opErr := export.Setattr(request.Node, request.Handle, *request.Change)
		err = opErr
		if err == nil {
			reply.Attr = &attr
		}
	case "statfs":
		stat, opErr := export.Statfs()
		err = opErr
		if err == nil {
			reply.Statfs = &stat
		}
	default:
		err = unix.ENOSYS
	}
	return reply, err
}

// DialFunc connects one guest mount to its VM's host volume service.
type DialFunc func(context.Context) (net.Conn, error)

// Client serializes calls in a stable, replayable session. Its state lives in
// the guest and survives native VM memory snapshots.
type Client struct {
	ctx        context.Context
	cancel     context.CancelFunc
	gate       chan struct{}
	mu         sync.Mutex
	conn       net.Conn
	dial       DialFunc
	descriptor Descriptor
	session    string
	sequence   uint64
}

// NewClient creates a mount-scoped client. Cancellation ends the whole mount's
// transport lifetime; it must not use an individual container Create deadline.
func NewClient(ctx context.Context, descriptor Descriptor, dial DialFunc) (*Client, error) {
	if !validToken(descriptor.Token) || dial == nil {
		return nil, unix.EINVAL
	}
	session, err := randomToken()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	client := &Client{ctx: ctx, cancel: cancel, gate: make(chan struct{}, 1), dial: dial, descriptor: descriptor, session: session}
	client.gate <- struct{}{}
	context.AfterFunc(ctx, func() { _ = client.Close() })
	return client, nil
}

// Close interrupts pending IO and prevents further calls. The owning shim must
// also unregister the export to release any handles from interrupted requests.
func (c *Client) Close() error {
	c.cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		err := c.conn.Close()
		c.conn = nil
		return err
	}
	return nil
}

func (c *Client) connection() (net.Conn, error) {
	c.mu.Lock()
	if c.ctx.Err() != nil {
		c.mu.Unlock()
		return nil, c.ctx.Err()
	}
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		return conn, nil
	}
	ctx, cancel := context.WithTimeout(c.ctx, rpcTimeout)
	defer cancel()
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctx.Err() != nil {
		_ = conn.Close()
		return nil, c.ctx.Err()
	}
	c.conn = conn
	return conn, nil
}

func (c *Client) drop(conn net.Conn) {
	_ = conn.Close()
	c.mu.Lock()
	if c.conn == conn {
		c.conn = nil
	}
	c.mu.Unlock()
}

// Call honors cancellation before issuing a request. Once issued, it resolves
// the result using the mount lifetime, retrying the SAME bytes and ID after a
// transport reset. Interrupting an in-flight mutation must not abandon a replay
// record or leave the following call uncertain about which operation completed.
func (c *Client) Call(ctx context.Context, request Request) (Reply, error) {
	select {
	case <-ctx.Done():
		return Reply{}, ctx.Err()
	case <-c.ctx.Done():
		return Reply{}, c.ctx.Err()
	case <-c.gate:
	}
	defer func() { c.gate <- struct{}{} }()
	if err := ctx.Err(); err != nil {
		return Reply{}, err
	}
	if c.sequence == ^uint64(0) {
		return Reply{}, unix.EOVERFLOW
	}
	request.Version, request.Token, request.Session, request.ID = ProtocolVersion, c.descriptor.Token, c.session, c.sequence+1
	frame, err := json.Marshal(request)
	if err != nil {
		return Reply{}, err
	}
	if len(frame) > MaxFrame {
		return Reply{}, unix.E2BIG
	}
	c.sequence++
	for {
		conn, err := c.connection()
		if err == nil {
			var data []byte
			err = conn.SetDeadline(time.Now().Add(rpcTimeout))
			if err == nil {
				err = writeFrame(conn, frame)
			}
			if err == nil {
				data, err = readFrame(conn)
			}
			if err == nil {
				var reply Reply
				if json.Unmarshal(data, &reply) != nil || reply.ID != request.ID || reply.Status > 4095 {
					_ = c.Close()
					return Reply{}, unix.EPROTO
				}
				if reply.Fatal {
					_ = c.Close()
				}
				if reply.Status != 0 {
					return reply, unix.Errno(reply.Status)
				}
				if reply.Fatal {
					return reply, unix.EPROTO
				}
				return reply, nil
			}
			c.drop(conn)
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-c.ctx.Done():
			timer.Stop()
			return Reply{}, c.ctx.Err()
		case <-timer.C:
		}
	}
}
