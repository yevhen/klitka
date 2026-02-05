package daemon

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"sync"

	"github.com/fxamacker/cbor/v2"
)

const maxVirtioFrame = 4 * 1024 * 1024

var (
	virtioEncMode cbor.EncMode
	virtioDecMode cbor.DecMode
)

func init() {
	enc, _ := cbor.CanonicalEncOptions().EncMode()
	dec, _ := cbor.DecOptions{DefaultMapType: reflect.TypeOf(map[string]interface{}{})}.DecMode()
	virtioEncMode = enc
	virtioDecMode = dec
}

type virtioClient struct {
	conn   net.Conn
	reader *bufio.Reader

	sendMu sync.Mutex
	mu     sync.Mutex
	nextID uint32
	reqs   map[uint32]*virtioRequest
	closed bool
}

type virtioRequest struct {
	id     uint32
	output chan virtioOutput
	exit   chan virtioExit
	err    chan error
}

type virtioOutput struct {
	stream string
	data   []byte
}

type virtioExit struct {
	code   int32
	signal *int32
}

func newVirtioClient(conn net.Conn) *virtioClient {
	client := &virtioClient{
		conn:   conn,
		reader: bufio.NewReader(conn),
		reqs:   make(map[uint32]*virtioRequest),
	}
	go client.readLoop()
	return client
}

func (c *virtioClient) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	for _, req := range c.reqs {
		close(req.output)
		close(req.exit)
		close(req.err)
	}
	c.reqs = map[uint32]*virtioRequest{}
	c.mu.Unlock()
	return c.conn.Close()
}

func (c *virtioClient) startExec(cmd string, args []string, stdin bool, pty bool) (*virtioRequest, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	req := &virtioRequest{
		id:     id,
		output: make(chan virtioOutput, 32),
		exit:   make(chan virtioExit, 1),
		err:    make(chan error, 1),
	}
	c.reqs[id] = req
	c.mu.Unlock()

	payload := map[string]interface{}{
		"cmd": cmd,
	}
	if len(args) > 0 {
		payload["argv"] = args
	}
	if stdin {
		payload["stdin"] = true
	}
	if pty {
		payload["pty"] = true
	}

	msg := map[string]interface{}{
		"v":  1,
		"t":  "exec_request",
		"id": id,
		"p":  payload,
	}
	if err := c.sendMessage(msg); err != nil {
		c.mu.Lock()
		delete(c.reqs, id)
		c.mu.Unlock()
		return nil, err
	}
	return req, nil
}

func (c *virtioClient) sendStdin(id uint32, data []byte, eof bool) error {
	payload := map[string]interface{}{
		"data": data,
	}
	if eof {
		payload["eof"] = true
	}
	msg := map[string]interface{}{
		"v":  1,
		"t":  "stdin_data",
		"id": id,
		"p":  payload,
	}
	return c.sendMessage(msg)
}

func (c *virtioClient) sendResize(id uint32, rows uint32, cols uint32) error {
	msg := map[string]interface{}{
		"v":  1,
		"t":  "pty_resize",
		"id": id,
		"p": map[string]interface{}{
			"rows": rows,
			"cols": cols,
		},
	}
	return c.sendMessage(msg)
}

func (c *virtioClient) sendMessage(message map[string]interface{}) error {
	payload, err := virtioEncMode.Marshal(message)
	if err != nil {
		return err
	}
	frame := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(payload)))
	copy(frame[4:], payload)

	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	_, err = c.conn.Write(frame)
	return err
}

func (c *virtioClient) readLoop() {
	for {
		frame, err := readFrame(c.reader)
		if err != nil {
			c.broadcastError(err)
			return
		}
		msg, err := decodeVirtioMessage(frame)
		if err != nil {
			c.broadcastError(err)
			return
		}
		c.dispatch(msg)
	}
}

func (c *virtioClient) broadcastError(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, req := range c.reqs {
		select {
		case req.err <- err:
		default:
		}
	}
}

func (c *virtioClient) dispatch(msg *virtioMessage) {
	c.mu.Lock()
	req, ok := c.reqs[msg.id]
	if !ok {
		c.mu.Unlock()
		return
	}
	if msg.typ == "exec_response" || msg.typ == "error" {
		delete(c.reqs, msg.id)
	}
	c.mu.Unlock()

	switch msg.typ {
	case "exec_output":
		output := virtioOutput{stream: msg.stream, data: msg.data}
		select {
		case req.output <- output:
		default:
		}
	case "exec_response":
		req.exit <- virtioExit{code: msg.exitCode, signal: msg.signal}
		close(req.output)
		close(req.exit)
		close(req.err)
	case "error":
		req.err <- fmt.Errorf("virtio error: %s", msg.errMessage)
		close(req.output)
		close(req.exit)
		close(req.err)
	}
}

type virtioMessage struct {
	typ        string
	id         uint32
	stream     string
	data       []byte
	exitCode   int32
	signal     *int32
	errMessage string
}

func readFrame(reader *bufio.Reader) ([]byte, error) {
	lenBuf := make([]byte, 4)
	if _, err := io.ReadFull(reader, lenBuf); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(lenBuf)
	if length > maxVirtioFrame {
		return nil, fmt.Errorf("virtio frame too large: %d", length)
	}
	frame := make([]byte, length)
	if _, err := io.ReadFull(reader, frame); err != nil {
		return nil, err
	}
	return frame, nil
}

func decodeVirtioMessage(frame []byte) (*virtioMessage, error) {
	var raw map[string]interface{}
	if err := virtioDecMode.Unmarshal(frame, &raw); err != nil {
		return nil, err
	}
	msgType, _ := raw["t"].(string)
	id, err := readUint32(raw["id"])
	if err != nil {
		return nil, err
	}
	payload, _ := raw["p"].(map[string]interface{})
	msg := &virtioMessage{typ: msgType, id: id}

	switch msgType {
	case "exec_output":
		msg.stream, _ = payload["stream"].(string)
		msg.data, _ = payload["data"].([]byte)
	case "exec_response":
		code, err := readInt32(payload["exit_code"])
		if err != nil {
			return nil, err
		}
		msg.exitCode = code
		if sig, ok := payload["signal"]; ok {
			val, err := readInt32(sig)
			if err == nil {
				msg.signal = &val
			}
		}
	case "error":
		if payload != nil {
			if message, ok := payload["message"].(string); ok {
				msg.errMessage = message
			}
		}
	default:
		return nil, fmt.Errorf("unknown virtio message type: %s", msgType)
	}

	return msg, nil
}

func readUint32(value interface{}) (uint32, error) {
	switch v := value.(type) {
	case uint64:
		return uint32(v), nil
	case uint32:
		return v, nil
	case int64:
		if v < 0 {
			return 0, errors.New("negative id")
		}
		return uint32(v), nil
	case int:
		if v < 0 {
			return 0, errors.New("negative id")
		}
		return uint32(v), nil
	default:
		return 0, fmt.Errorf("unexpected id type %T", value)
	}
}

func readInt32(value interface{}) (int32, error) {
	switch v := value.(type) {
	case uint64:
		return int32(v), nil
	case int64:
		return int32(v), nil
	case int:
		return int32(v), nil
	default:
		return 0, fmt.Errorf("unexpected int type %T", value)
	}
}

func decodeVirtioError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, io.EOF) {
		return io.EOF
	}
	if errors.Is(err, net.ErrClosed) {
		return net.ErrClosed
	}
	return err
}

func collectExecOutput(req *virtioRequest) (stdout, stderr []byte, exit int32, err error) {
	var outBuf bytes.Buffer
	var errBuf bytes.Buffer

	for {
		select {
		case output, ok := <-req.output:
			if ok {
				if output.stream == "stderr" {
					_, _ = errBuf.Write(output.data)
				} else {
					_, _ = outBuf.Write(output.data)
				}
			}
		case exitMsg, ok := <-req.exit:
			if ok {
				return outBuf.Bytes(), errBuf.Bytes(), exitMsg.code, nil
			}
		case reqErr, ok := <-req.err:
			if ok {
				if reqErr != nil {
					return outBuf.Bytes(), errBuf.Bytes(), 1, reqErr
				}
			}
		}
	}
}
