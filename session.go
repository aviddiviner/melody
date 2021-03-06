package melody

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var (
	ErrWriteToClosedSession = errors.New("tried to write to a closed session")
	ErrMessageBufferFull    = errors.New("session message buffer is full")
	ErrSessionClosed        = errors.New("session is closed")
	ErrSessionAlreadyClosed = errors.New("session is already closed")
)

// Session wrapper around websocket connections.
type Session struct {
	Request *http.Request
	conn    *websocket.Conn
	output  chan *envelope
	melody  *Melody
	open    bool
	rwmutex *sync.RWMutex
}

func (s *Session) writeMessage(message *envelope) error {
	if s.closed() {
		s.melody.errorHandler(s, ErrWriteToClosedSession)
		return ErrWriteToClosedSession
	}

	select {
	case s.output <- message:
	default:
		s.melody.errorHandler(s, ErrMessageBufferFull)
		return ErrMessageBufferFull
	}

	return nil
}

func (s *Session) writeRaw(message *envelope) error {
	if s.closed() {
		return ErrWriteToClosedSession
	}

	s.conn.SetWriteDeadline(time.Now().Add(s.melody.Config.WriteWait))
	err := s.conn.WriteMessage(message.t, message.msg)

	if err != nil {
		return err
	}

	return nil
}

func (s *Session) closed() bool {
	s.rwmutex.RLock()
	defer s.rwmutex.RUnlock()

	return !s.open
}

func (s *Session) close() {
	if !s.closed() {
		s.rwmutex.Lock()
		s.open = false
		s.conn.Close()
		close(s.output)
		s.rwmutex.Unlock()
	}
}

func (s *Session) ping() {
	s.writeRaw(&envelope{t: websocket.PingMessage, msg: []byte{}})
}

func (s *Session) writePump() {
	ticker := time.NewTicker(s.melody.Config.PingPeriod)
	defer ticker.Stop()

loop:
	for {
		select {
		case msg, ok := <-s.output:
			if !ok {
				break loop
			}

			err := s.writeRaw(msg)

			if err != nil {
				s.melody.errorHandler(s, err)
				break loop
			}

			if msg.t == websocket.CloseMessage {
				break loop
			}

			if msg.t == websocket.TextMessage {
				s.melody.messageSentHandler(s, msg.msg)
			}

			if msg.t == websocket.BinaryMessage {
				s.melody.messageSentHandlerBinary(s, msg.msg)
			}
		case <-ticker.C:
			s.ping()
		}
	}
}

func (s *Session) readPump() {
	s.conn.SetReadLimit(s.melody.Config.MaxMessageSize)
	s.conn.SetReadDeadline(time.Now().Add(s.melody.Config.PongWait))

	s.conn.SetPongHandler(func(string) error {
		s.conn.SetReadDeadline(time.Now().Add(s.melody.Config.PongWait))
		s.melody.pongHandler(s)
		return nil
	})

	if s.melody.closeHandler != nil {
		s.conn.SetCloseHandler(func(code int, text string) error {
			return s.melody.closeHandler(s, code, text)
		})
	}

	for {
		t, message, err := s.conn.ReadMessage()

		if err != nil {
			s.melody.errorHandler(s, err)
			break
		}

		if t == websocket.TextMessage {
			s.melody.messageHandler(s, message)
		}

		if t == websocket.BinaryMessage {
			s.melody.messageHandlerBinary(s, message)
		}
	}
}

// Write writes message to session.
func (s *Session) Write(msg []byte) (n int, err error) {
	if s.closed() {
		return 0, ErrSessionClosed
	}

	err = s.writeMessage(&envelope{t: websocket.TextMessage, msg: msg})
	if err == nil {
		n = len(msg)
	}
	return
}

// WriteBinary writes a binary message to session.
func (s *Session) WriteBinary(msg []byte) error {
	if s.closed() {
		return ErrSessionClosed
	}

	return s.writeMessage(&envelope{t: websocket.BinaryMessage, msg: msg})
}

// Close closes session.
func (s *Session) Close() error {
	if s.closed() {
		return ErrSessionAlreadyClosed
	}

	return s.writeMessage(&envelope{t: websocket.CloseMessage, msg: []byte{}})
}

// CloseWithMsg closes the session with the provided payload.
// Use the FormatCloseMessage function to format a proper close message payload.
func (s *Session) CloseWithMsg(msg []byte) error {
	if s.closed() {
		return ErrSessionAlreadyClosed
	}

	return s.writeMessage(&envelope{t: websocket.CloseMessage, msg: msg})
}

// Set is used to store a new key/value pair exclusivelly for this session.
// It also lazy initializes s.Keys if it was not used previously.
func (s *Session) Set(key string, value interface{}) {
	s.Request = newRequestWithContextKey(s.Request, key, value)
}

// Get returns the value for the given key, ie: (value, true).
// If the value does not exists it returns (nil, false)
func (s *Session) Get(key string) (value interface{}, exists bool) {
	value = s.Request.Context().Value(contextKey(key))
	return value, value != nil
}

// MustGet returns the value for the given key if it exists, otherwise it panics.
func (s *Session) MustGet(key string) interface{} {
	if value, exists := s.Get(key); exists {
		return value
	}

	panic("Key \"" + key + "\" does not exist")
}

// IsClosed returns the status of the connection.
func (s *Session) IsClosed() bool {
	return s.closed()
}
