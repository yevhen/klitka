package daemon

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	klitkav1 "github.com/yevhen/klitka/proto/gen/go/klitka/v1"
)

const (
	linuxErrNoent  int32 = 2
	linuxErrIO     int32 = 5
	linuxErrBadf   int32 = 9
	linuxErrAcces  int32 = 13
	linuxErrExist  int32 = 17
	linuxErrNotDir int32 = 20
	linuxErrIsDir  int32 = 21
	linuxErrInval  int32 = 22
	linuxErrROFS   int32 = 30
	linuxErrNOSYS  int32 = 38
	linuxErrLoop   int32 = 40
)

const (
	linuxOAccMode = 0x3
	linuxOWrOnly  = 0x1
	linuxORDWR    = 0x2
	linuxOCreat   = 0x40
	linuxOExcl    = 0x80
	linuxOTrunc   = 0x200
	linuxOAppend  = 0x400
)

const maxFSRPCReadSize = 256 * 1024

var (
	fsrpcMaxHandleEntries = 2048
	fsrpcHandleIdleTTL    = 2 * time.Minute
	fsrpcMaxInodeEntries  = 8192
	fsrpcGCInterval       = 15 * time.Second
)

const (
	linuxDTUnknown uint32 = 0
	linuxDTDir     uint32 = 4
	linuxDTReg     uint32 = 8
	linuxDTLnk     uint32 = 10
)

type fsrpcServer struct {
	conn      net.Conn
	reader    *bufio.Reader
	mount     *fsrpcMount
	metrics   fsrpcServerMetrics
	closeOnce sync.Once
}

type fsrpcOpMetrics struct {
	count        uint64
	errors       uint64
	reqBytes     uint64
	resBytes     uint64
	latencyTotal time.Duration
	latencyMax   time.Duration
}

type fsrpcServerMetrics struct {
	started       time.Time
	totalRequests uint64
	totalErrors   uint64
	totalReqBytes uint64
	totalResBytes uint64
	ops           map[string]fsrpcOpMetrics
}

type fsrpcHandle struct {
	file     *os.File
	append   bool
	ino      uint64
	lastUsed time.Time
}

type fsrpcMount struct {
	root string
	mode klitkav1.MountMode

	mu        sync.Mutex
	nextIno   uint64
	nextFH    uint64
	pathToIno map[string]uint64
	inoToPath map[uint64]string
	handles   map[uint64]*fsrpcHandle
	lastGC    time.Time
}

func newFSRPCMount(root string, mode klitkav1.MountMode) *fsrpcMount {
	absRoot := root
	if abs, err := filepath.Abs(root); err == nil {
		absRoot = abs
	}
	absRoot = filepath.Clean(absRoot)

	pathToIno := map[string]uint64{absRoot: 1}
	inoToPath := map[uint64]string{1: absRoot}

	return &fsrpcMount{
		root:      absRoot,
		mode:      mode,
		nextIno:   2,
		nextFH:    1,
		pathToIno: pathToIno,
		inoToPath: inoToPath,
		handles:   map[uint64]*fsrpcHandle{},
		lastGC:    time.Now(),
	}
}

func (mount *fsrpcMount) close() {
	mount.mu.Lock()
	handles := make([]*fsrpcHandle, 0, len(mount.handles))
	for fh, handle := range mount.handles {
		if handle != nil {
			handles = append(handles, handle)
		}
		delete(mount.handles, fh)
	}
	mount.mu.Unlock()

	for _, handle := range handles {
		if handle != nil && handle.file != nil {
			_ = handle.file.Close()
		}
	}
}

func newFSRPCServer(conn net.Conn, mount *fsrpcMount) *fsrpcServer {
	return &fsrpcServer{
		conn:    conn,
		reader:  bufio.NewReader(conn),
		mount:   mount,
		metrics: newFSRPCServerMetrics(),
	}
}

func newFSRPCServerMetrics() fsrpcServerMetrics {
	return fsrpcServerMetrics{
		started: time.Now(),
		ops:     map[string]fsrpcOpMetrics{},
	}
}

func (metrics *fsrpcServerMetrics) observe(op string, reqBytes int, resBytes int, errCode int32, latency time.Duration) {
	metrics.totalRequests++
	if errCode != 0 {
		metrics.totalErrors++
	}
	if reqBytes > 0 {
		metrics.totalReqBytes += uint64(reqBytes)
	}
	if resBytes > 0 {
		metrics.totalResBytes += uint64(resBytes)
	}

	opMetrics := metrics.ops[op]
	opMetrics.count++
	if errCode != 0 {
		opMetrics.errors++
	}
	if reqBytes > 0 {
		opMetrics.reqBytes += uint64(reqBytes)
	}
	if resBytes > 0 {
		opMetrics.resBytes += uint64(resBytes)
	}
	opMetrics.latencyTotal += latency
	if latency > opMetrics.latencyMax {
		opMetrics.latencyMax = latency
	}
	metrics.ops[op] = opMetrics
}

func formatFSRPCOpMetrics(metrics map[string]fsrpcOpMetrics) string {
	if len(metrics) == 0 {
		return "-"
	}

	names := make([]string, 0, len(metrics))
	for name := range metrics {
		names = append(names, name)
	}
	sort.Strings(names)

	parts := make([]string, 0, len(names))
	for _, name := range names {
		stat := metrics[name]
		avg := int64(0)
		if stat.count > 0 {
			avg = stat.latencyTotal.Milliseconds() / int64(stat.count)
		}
		parts = append(parts, fmt.Sprintf("%s[count=%d errors=%d avg_ms=%d max_ms=%d req_bytes=%d res_bytes=%d]",
			name,
			stat.count,
			stat.errors,
			avg,
			stat.latencyMax.Milliseconds(),
			stat.reqBytes,
			stat.resBytes,
		))
	}
	return strings.Join(parts, ",")
}

func (server *fsrpcServer) serve() error {
	defer server.Close()
	defer func() {
		uptime := time.Since(server.metrics.started)
		log.Printf(
			"fsrpc status=summary mount=%q requests=%d errors=%d req_bytes=%d res_bytes=%d uptime_ms=%d ops=%s",
			server.mount.root,
			server.metrics.totalRequests,
			server.metrics.totalErrors,
			server.metrics.totalReqBytes,
			server.metrics.totalResBytes,
			uptime.Milliseconds(),
			formatFSRPCOpMetrics(server.metrics.ops),
		)
	}()

	for {
		frame, err := readFrame(server.reader)
		if err != nil {
			if err == io.EOF || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}

		req, err := decodeFSRPCRequest(frame)
		if err != nil {
			log.Printf("fsrpc status=decode_error mount=%q err=%q frame_bytes=%d", server.mount.root, err.Error(), len(frame))
			return err
		}

		start := time.Now()
		res, errCode, message := server.mount.handle(req.op, req.args)
		if errCode != 0 && shouldLogFSRPCOpError(req.op, errCode) {
			log.Printf("fsrpc status=op_error mount=%q op=%q req_id=%d errno=%d message=%q", server.mount.root, req.op, req.id, errCode, message)
		}
		responseBytes, sendErr := server.sendResponse(req.id, req.op, errCode, res, message)
		if sendErr != nil {
			server.metrics.observe(req.op, len(frame), 0, linuxErrIO, time.Since(start))
			log.Printf("fsrpc status=send_error mount=%q op=%q req_id=%d err=%q", server.mount.root, req.op, req.id, sendErr.Error())
			return sendErr
		}
		server.metrics.observe(req.op, len(frame), responseBytes, errCode, time.Since(start))
	}
}

func (server *fsrpcServer) sendResponse(id uint32, op string, errCode int32, res map[string]interface{}, message string) (int, error) {
	payload := map[string]interface{}{
		"op":  op,
		"err": errCode,
	}
	if res != nil {
		payload["res"] = res
	} else {
		payload["res"] = nil
	}
	if message != "" {
		payload["message"] = message
	} else {
		payload["message"] = nil
	}

	msg := map[string]interface{}{
		"v":  uint64(1),
		"t":  "fs_response",
		"id": id,
		"p":  payload,
	}

	encoded, err := virtioEncMode.Marshal(msg)
	if err != nil {
		return 0, err
	}

	frame := make([]byte, 4+len(encoded))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(encoded)))
	copy(frame[4:], encoded)

	if err := writeAllConn(server.conn, frame); err != nil {
		return 0, err
	}
	return len(frame), nil
}

func writeAllConn(conn net.Conn, data []byte) error {
	for len(data) > 0 {
		n, err := conn.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func (server *fsrpcServer) Close() error {
	var err error
	server.closeOnce.Do(func() {
		if server.mount != nil {
			server.mount.close()
		}
		if server.conn != nil {
			err = server.conn.Close()
		}
	})
	return err
}

type fsrpcRequest struct {
	id   uint32
	op   string
	args map[string]interface{}
}

func decodeFSRPCRequest(frame []byte) (fsrpcRequest, error) {
	var raw map[string]interface{}
	if err := virtioDecMode.Unmarshal(frame, &raw); err != nil {
		return fsrpcRequest{}, err
	}

	typ, _ := raw["t"].(string)
	if typ != "fs_request" {
		return fsrpcRequest{}, fmt.Errorf("unexpected fsrpc message type: %q", typ)
	}

	id, err := readUint32(raw["id"])
	if err != nil {
		return fsrpcRequest{}, err
	}

	payload, _ := raw["p"].(map[string]interface{})
	if payload == nil {
		return fsrpcRequest{}, fmt.Errorf("missing fsrpc payload")
	}
	op, _ := payload["op"].(string)
	if strings.TrimSpace(op) == "" {
		return fsrpcRequest{}, fmt.Errorf("missing fsrpc op")
	}
	args, _ := payload["req"].(map[string]interface{})
	if args == nil {
		args = map[string]interface{}{}
	}

	return fsrpcRequest{id: id, op: op, args: args}, nil
}

func shouldLogFSRPCOpError(op string, errCode int32) bool {
	if errCode == linuxErrNoent {
		switch op {
		case "lookup", "open", "getattr", "unlink", "rename", "read", "truncate", "release":
			return false
		}
	}
	if errCode == linuxErrExist {
		switch op {
		case "create", "mkdir":
			return false
		}
	}
	return true
}

func (mount *fsrpcMount) handle(op string, req map[string]interface{}) (map[string]interface{}, int32, string) {
	mount.maybeGC(time.Now())

	switch op {
	case "lookup":
		return mount.handleLookup(req)
	case "getattr":
		return mount.handleGetattr(req)
	case "readdir":
		return mount.handleReaddir(req)
	case "open":
		return mount.handleOpen(req)
	case "read":
		return mount.handleRead(req)
	case "release":
		return mount.handleRelease(req)
	case "ping":
		return map[string]interface{}{
			"ok":   true,
			"mode": mount.modeLabel(),
		}, 0, ""
	case "write":
		if mount.readOnly() {
			return nil, linuxErrROFS, "read-only mount"
		}
		return mount.handleWrite(req)
	case "create":
		if mount.readOnly() {
			return nil, linuxErrROFS, "read-only mount"
		}
		return mount.handleCreate(req)
	case "mkdir":
		if mount.readOnly() {
			return nil, linuxErrROFS, "read-only mount"
		}
		return mount.handleMkdir(req)
	case "unlink":
		if mount.readOnly() {
			return nil, linuxErrROFS, "read-only mount"
		}
		return mount.handleUnlink(req)
	case "rename":
		if mount.readOnly() {
			return nil, linuxErrROFS, "read-only mount"
		}
		return mount.handleRename(req)
	case "truncate":
		if mount.readOnly() {
			return nil, linuxErrROFS, "read-only mount"
		}
		return mount.handleTruncate(req)
	default:
		return nil, linuxErrNOSYS, "unsupported op"
	}
}

func (mount *fsrpcMount) handleLookup(req map[string]interface{}) (map[string]interface{}, int32, string) {
	parentIno, ok := getReqUint(req, "parent_ino")
	if !ok {
		return nil, linuxErrInval, "parent_ino required"
	}
	name, ok := getReqString(req, "name")
	if !ok {
		return nil, linuxErrInval, "name required"
	}

	parentPath, ok := mount.pathForInode(parentIno)
	if !ok {
		return nil, linuxErrNotDir, "parent inode not found"
	}
	targetPath, errCode, errMsg := mount.resolveChildPath(parentPath, name)
	if errCode != 0 {
		return nil, errCode, errMsg
	}

	info, err := os.Lstat(targetPath)
	if err != nil {
		return nil, mapOSError(err), err.Error()
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, linuxErrLoop, "symlink traversal is not allowed"
	}

	ino := mount.ensureInode(targetPath)
	attr := mount.buildAttr(ino, info)

	return map[string]interface{}{
		"entry": map[string]interface{}{
			"ino":          ino,
			"attr":         attr,
			"attr_ttl_ms":  uint64(1000),
			"entry_ttl_ms": uint64(1000),
		},
	}, 0, ""
}

func (mount *fsrpcMount) handleGetattr(req map[string]interface{}) (map[string]interface{}, int32, string) {
	ino, ok := getReqUint(req, "ino")
	if !ok {
		return nil, linuxErrInval, "ino required"
	}

	path, ok := mount.pathForInode(ino)
	if !ok {
		return nil, linuxErrInval, "inode not found"
	}

	info, err := os.Lstat(path)
	if err != nil {
		return nil, mapOSError(err), err.Error()
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, linuxErrLoop, "symlink traversal is not allowed"
	}

	return map[string]interface{}{
		"attr":        mount.buildAttr(ino, info),
		"attr_ttl_ms": uint64(1000),
	}, 0, ""
}

func (mount *fsrpcMount) handleReaddir(req map[string]interface{}) (map[string]interface{}, int32, string) {
	ino, ok := getReqUint(req, "ino")
	if !ok {
		return nil, linuxErrInval, "ino required"
	}
	offset, _ := getReqUint(req, "offset")
	maxEntries, ok := getReqUint(req, "max_entries")
	if !ok || maxEntries == 0 {
		maxEntries = 128
	}

	dirPath, ok := mount.pathForInode(ino)
	if !ok {
		return nil, linuxErrInval, "inode not found"
	}
	info, err := os.Lstat(dirPath)
	if err != nil {
		return nil, mapOSError(err), err.Error()
	}
	if !info.IsDir() {
		return nil, linuxErrNotDir, "not a directory"
	}

	dirEntries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, mapOSError(err), err.Error()
	}

	entries := make([]map[string]interface{}, 0, len(dirEntries)+2)
	entries = append(entries, map[string]interface{}{"ino": ino, "name": ".", "type": linuxDTDir})
	parentIno := ino
	if dirPath != mount.root {
		parentPath := filepath.Dir(dirPath)
		parentIno = mount.ensureInode(parentPath)
	}
	entries = append(entries, map[string]interface{}{"ino": parentIno, "name": "..", "type": linuxDTDir})

	for _, entry := range dirEntries {
		name := entry.Name()
		targetPath := filepath.Join(dirPath, name)
		targetIno := mount.ensureInode(targetPath)
		entries = append(entries, map[string]interface{}{
			"ino":  targetIno,
			"name": name,
			"type": dtypeFromFileMode(entry.Type()),
		})
	}

	start := int(offset)
	if start < 0 {
		start = 0
	}
	if start >= len(entries) {
		return map[string]interface{}{"entries": []map[string]interface{}{}}, 0, ""
	}

	end := start + int(maxEntries)
	if end > len(entries) {
		end = len(entries)
	}

	result := make([]map[string]interface{}, 0, end-start)
	for idx := start; idx < end; idx++ {
		item := entries[idx]
		item["offset"] = uint64(idx + 1)
		result = append(result, item)
	}

	return map[string]interface{}{"entries": result}, 0, ""
}

func (mount *fsrpcMount) handleOpen(req map[string]interface{}) (map[string]interface{}, int32, string) {
	ino, ok := getReqUint(req, "ino")
	if !ok {
		return nil, linuxErrInval, "ino required"
	}
	flags, _ := getReqUint(req, "flags")

	if mount.readOnly() && hasWriteIntent(flags) {
		return nil, linuxErrROFS, "read-only mount"
	}

	path, ok := mount.pathForInode(ino)
	if !ok {
		return nil, linuxErrInval, "inode not found"
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, mapOSError(err), err.Error()
	}
	if info.Mode().IsDir() {
		return nil, linuxErrIsDir, "is a directory"
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, linuxErrLoop, "symlink traversal is not allowed"
	}

	file, err := os.OpenFile(path, osOpenFlagsFromLinux(flags), 0)
	if err != nil {
		return nil, mapOSError(err), err.Error()
	}

	now := time.Now()
	mount.mu.Lock()
	fh := mount.nextHandleIDLocked()
	mount.handles[fh] = &fsrpcHandle{file: file, append: flags&linuxOAppend != 0, ino: ino, lastUsed: now}
	mount.mu.Unlock()

	return map[string]interface{}{"fh": fh, "open_flags": uint64(0)}, 0, ""
}

func (mount *fsrpcMount) handleRead(req map[string]interface{}) (map[string]interface{}, int32, string) {
	fh, ok := getReqUint(req, "fh")
	if !ok {
		return nil, linuxErrInval, "fh required"
	}
	offset, _ := getReqUint(req, "offset")
	size, ok := getReqUint(req, "size")
	if !ok {
		return nil, linuxErrInval, "size required"
	}
	if size > maxFSRPCReadSize {
		size = maxFSRPCReadSize
	}

	handle := mount.getHandle(fh)
	if handle == nil || handle.file == nil {
		return nil, linuxErrBadf, "invalid file handle"
	}

	buf := make([]byte, size)
	n, err := handle.file.ReadAt(buf, int64(offset))
	if err != nil && err != io.EOF {
		return nil, mapOSError(err), err.Error()
	}

	return map[string]interface{}{"data": buf[:n]}, 0, ""
}

func (mount *fsrpcMount) handleRelease(req map[string]interface{}) (map[string]interface{}, int32, string) {
	fh, ok := getReqUint(req, "fh")
	if !ok {
		return nil, linuxErrInval, "fh required"
	}

	handle := mount.closeHandle(fh)
	if handle == nil || handle.file == nil {
		return nil, linuxErrBadf, "invalid file handle"
	}
	return map[string]interface{}{}, 0, ""
}

func (mount *fsrpcMount) handleWrite(req map[string]interface{}) (map[string]interface{}, int32, string) {
	fh, ok := getReqUint(req, "fh")
	if !ok {
		return nil, linuxErrInval, "fh required"
	}
	offset, ok := getReqUint(req, "offset")
	if !ok {
		return nil, linuxErrInval, "offset required"
	}
	data, ok := getReqBytes(req, "data")
	if !ok {
		return nil, linuxErrInval, "data required"
	}

	handle := mount.getHandle(fh)
	if handle == nil || handle.file == nil {
		return nil, linuxErrBadf, "invalid file handle"
	}

	written := 0
	var err error
	if handle.append {
		written, err = handle.file.Write(data)
	} else {
		written, err = handle.file.WriteAt(data, int64(offset))
	}
	if err != nil {
		return nil, mapOSError(err), err.Error()
	}

	return map[string]interface{}{"size": uint64(written)}, 0, ""
}

func (mount *fsrpcMount) handleCreate(req map[string]interface{}) (map[string]interface{}, int32, string) {
	parentIno, ok := getReqUint(req, "parent_ino")
	if !ok {
		return nil, linuxErrInval, "parent_ino required"
	}
	name, ok := getReqString(req, "name")
	if !ok {
		return nil, linuxErrInval, "name required"
	}
	mode, _ := getReqUint(req, "mode")
	flags, _ := getReqUint(req, "flags")

	parentPath, ok := mount.pathForInode(parentIno)
	if !ok {
		return nil, linuxErrNotDir, "parent inode not found"
	}
	parentInfo, err := os.Lstat(parentPath)
	if err != nil {
		return nil, mapOSError(err), err.Error()
	}
	if !parentInfo.IsDir() {
		return nil, linuxErrNotDir, "parent is not a directory"
	}
	if parentInfo.Mode()&os.ModeSymlink != 0 {
		return nil, linuxErrLoop, "symlink traversal is not allowed"
	}

	targetPath, errCode, errMsg := mount.resolveChildPath(parentPath, name)
	if errCode != 0 {
		return nil, errCode, errMsg
	}

	if info, statErr := os.Lstat(targetPath); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, linuxErrLoop, "symlink traversal is not allowed"
		}
		if info.IsDir() {
			return nil, linuxErrIsDir, "is a directory"
		}
	} else if !os.IsNotExist(statErr) {
		return nil, mapOSError(statErr), statErr.Error()
	}

	file, err := os.OpenFile(targetPath, osOpenFlagsFromLinux(flags)|os.O_CREATE, os.FileMode(mode&0o777))
	if err != nil {
		return nil, mapOSError(err), err.Error()
	}

	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, mapOSError(err), err.Error()
	}
	ino := mount.ensureInode(targetPath)

	now := time.Now()
	mount.mu.Lock()
	fh := mount.nextHandleIDLocked()
	mount.handles[fh] = &fsrpcHandle{file: file, append: flags&linuxOAppend != 0, ino: ino, lastUsed: now}
	mount.mu.Unlock()

	return map[string]interface{}{
		"entry": map[string]interface{}{
			"ino":          ino,
			"attr":         mount.buildAttr(ino, info),
			"attr_ttl_ms":  uint64(1000),
			"entry_ttl_ms": uint64(1000),
		},
		"fh":         fh,
		"open_flags": uint64(0),
	}, 0, ""
}

func (mount *fsrpcMount) handleMkdir(req map[string]interface{}) (map[string]interface{}, int32, string) {
	parentIno, ok := getReqUint(req, "parent_ino")
	if !ok {
		return nil, linuxErrInval, "parent_ino required"
	}
	name, ok := getReqString(req, "name")
	if !ok {
		return nil, linuxErrInval, "name required"
	}
	mode, _ := getReqUint(req, "mode")

	parentPath, ok := mount.pathForInode(parentIno)
	if !ok {
		return nil, linuxErrNotDir, "parent inode not found"
	}
	parentInfo, err := os.Lstat(parentPath)
	if err != nil {
		return nil, mapOSError(err), err.Error()
	}
	if !parentInfo.IsDir() {
		return nil, linuxErrNotDir, "parent is not a directory"
	}
	if parentInfo.Mode()&os.ModeSymlink != 0 {
		return nil, linuxErrLoop, "symlink traversal is not allowed"
	}

	targetPath, errCode, errMsg := mount.resolveChildPath(parentPath, name)
	if errCode != 0 {
		return nil, errCode, errMsg
	}

	if err := os.Mkdir(targetPath, os.FileMode(mode&0o777)); err != nil {
		return nil, mapOSError(err), err.Error()
	}

	info, err := os.Lstat(targetPath)
	if err != nil {
		return nil, mapOSError(err), err.Error()
	}
	ino := mount.ensureInode(targetPath)

	return map[string]interface{}{
		"entry": map[string]interface{}{
			"ino":          ino,
			"attr":         mount.buildAttr(ino, info),
			"attr_ttl_ms":  uint64(1000),
			"entry_ttl_ms": uint64(1000),
		},
	}, 0, ""
}

func (mount *fsrpcMount) handleUnlink(req map[string]interface{}) (map[string]interface{}, int32, string) {
	parentIno, ok := getReqUint(req, "parent_ino")
	if !ok {
		return nil, linuxErrInval, "parent_ino required"
	}
	name, ok := getReqString(req, "name")
	if !ok {
		return nil, linuxErrInval, "name required"
	}

	parentPath, ok := mount.pathForInode(parentIno)
	if !ok {
		return nil, linuxErrNotDir, "parent inode not found"
	}
	parentInfo, err := os.Lstat(parentPath)
	if err != nil {
		return nil, mapOSError(err), err.Error()
	}
	if !parentInfo.IsDir() {
		return nil, linuxErrNotDir, "parent is not a directory"
	}
	if parentInfo.Mode()&os.ModeSymlink != 0 {
		return nil, linuxErrLoop, "symlink traversal is not allowed"
	}

	targetPath, errCode, errMsg := mount.resolveChildPath(parentPath, name)
	if errCode != 0 {
		return nil, errCode, errMsg
	}

	info, err := os.Lstat(targetPath)
	if err != nil {
		return nil, mapOSError(err), err.Error()
	}
	if info.IsDir() {
		return nil, linuxErrIsDir, "is a directory"
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, linuxErrLoop, "symlink traversal is not allowed"
	}

	if err := os.Remove(targetPath); err != nil {
		return nil, mapOSError(err), err.Error()
	}
	mount.removePathMapping(targetPath)

	return map[string]interface{}{}, 0, ""
}

func (mount *fsrpcMount) handleRename(req map[string]interface{}) (map[string]interface{}, int32, string) {
	oldParentIno, ok := getReqUint(req, "old_parent_ino")
	if !ok {
		return nil, linuxErrInval, "old_parent_ino required"
	}
	oldName, ok := getReqString(req, "old_name")
	if !ok {
		return nil, linuxErrInval, "old_name required"
	}
	newParentIno, ok := getReqUint(req, "new_parent_ino")
	if !ok {
		return nil, linuxErrInval, "new_parent_ino required"
	}
	newName, ok := getReqString(req, "new_name")
	if !ok {
		return nil, linuxErrInval, "new_name required"
	}
	_, _ = getReqUint(req, "flags")

	oldParentPath, ok := mount.pathForInode(oldParentIno)
	if !ok {
		return nil, linuxErrNotDir, "old parent inode not found"
	}
	newParentPath, ok := mount.pathForInode(newParentIno)
	if !ok {
		return nil, linuxErrNotDir, "new parent inode not found"
	}

	oldParentInfo, err := os.Lstat(oldParentPath)
	if err != nil {
		return nil, mapOSError(err), err.Error()
	}
	if !oldParentInfo.IsDir() {
		return nil, linuxErrNotDir, "old parent is not a directory"
	}
	if oldParentInfo.Mode()&os.ModeSymlink != 0 {
		return nil, linuxErrLoop, "symlink traversal is not allowed"
	}

	newParentInfo, err := os.Lstat(newParentPath)
	if err != nil {
		return nil, mapOSError(err), err.Error()
	}
	if !newParentInfo.IsDir() {
		return nil, linuxErrNotDir, "new parent is not a directory"
	}
	if newParentInfo.Mode()&os.ModeSymlink != 0 {
		return nil, linuxErrLoop, "symlink traversal is not allowed"
	}

	oldPath, errCode, errMsg := mount.resolveChildPath(oldParentPath, oldName)
	if errCode != 0 {
		return nil, errCode, errMsg
	}
	newPath, errCode, errMsg := mount.resolveChildPath(newParentPath, newName)
	if errCode != 0 {
		return nil, errCode, errMsg
	}

	oldInfo, err := os.Lstat(oldPath)
	if err != nil {
		return nil, mapOSError(err), err.Error()
	}
	if oldInfo.Mode()&os.ModeSymlink != 0 {
		return nil, linuxErrLoop, "symlink traversal is not allowed"
	}

	if err := os.Rename(oldPath, newPath); err != nil {
		return nil, mapOSError(err), err.Error()
	}

	mount.remapPath(oldPath, newPath)
	return map[string]interface{}{}, 0, ""
}

func (mount *fsrpcMount) handleTruncate(req map[string]interface{}) (map[string]interface{}, int32, string) {
	ino, ok := getReqUint(req, "ino")
	if !ok {
		return nil, linuxErrInval, "ino required"
	}
	size, ok := getReqUint(req, "size")
	if !ok {
		return nil, linuxErrInval, "size required"
	}

	path, ok := mount.pathForInode(ino)
	if !ok {
		return nil, linuxErrInval, "inode not found"
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, mapOSError(err), err.Error()
	}
	if info.IsDir() {
		return nil, linuxErrIsDir, "is a directory"
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, linuxErrLoop, "symlink traversal is not allowed"
	}

	if err := os.Truncate(path, int64(size)); err != nil {
		return nil, mapOSError(err), err.Error()
	}
	return map[string]interface{}{}, 0, ""
}

func (mount *fsrpcMount) pathForInode(ino uint64) (string, bool) {
	mount.mu.Lock()
	defer mount.mu.Unlock()
	path, ok := mount.inoToPath[ino]
	return path, ok
}

func (mount *fsrpcMount) ensureInode(path string) uint64 {
	clean := filepath.Clean(path)

	mount.mu.Lock()
	defer mount.mu.Unlock()
	if ino, ok := mount.pathToIno[clean]; ok {
		return ino
	}
	ino := mount.nextIno
	mount.nextIno++
	mount.pathToIno[clean] = ino
	mount.inoToPath[ino] = clean
	return ino
}

func (mount *fsrpcMount) nextHandleIDLocked() uint64 {
	fh := mount.nextFH
	mount.nextFH++
	if mount.nextFH == 0 {
		mount.nextFH = 1
	}
	for {
		if _, exists := mount.handles[fh]; !exists {
			return fh
		}
		fh = mount.nextFH
		mount.nextFH++
		if mount.nextFH == 0 {
			mount.nextFH = 1
		}
	}
}

func (mount *fsrpcMount) getHandle(fh uint64) *fsrpcHandle {
	mount.mu.Lock()
	defer mount.mu.Unlock()
	handle := mount.handles[fh]
	if handle != nil {
		handle.lastUsed = time.Now()
	}
	return handle
}

func (mount *fsrpcMount) closeHandle(fh uint64) *fsrpcHandle {
	mount.mu.Lock()
	handle := mount.handles[fh]
	if handle != nil {
		delete(mount.handles, fh)
	}
	mount.mu.Unlock()
	if handle == nil {
		return nil
	}
	if handle.file != nil {
		_ = handle.file.Close()
	}
	return handle
}

func (mount *fsrpcMount) maybeGC(now time.Time) {
	mount.mu.Lock()
	if fsrpcGCInterval > 0 && now.Sub(mount.lastGC) < fsrpcGCInterval {
		mount.mu.Unlock()
		return
	}
	mount.lastGC = now
	toClose := mount.gcHandlesLocked(now)
	mount.gcInodesLocked()
	mount.mu.Unlock()

	for _, handle := range toClose {
		if handle != nil && handle.file != nil {
			_ = handle.file.Close()
		}
	}
}

func (mount *fsrpcMount) gcHandlesLocked(now time.Time) []*fsrpcHandle {
	if len(mount.handles) == 0 {
		return nil
	}

	toClose := make([]*fsrpcHandle, 0)
	staleCutoff := now.Add(-fsrpcHandleIdleTTL)

	for fh, handle := range mount.handles {
		if handle == nil || handle.file == nil {
			delete(mount.handles, fh)
			continue
		}
		if fsrpcHandleIdleTTL > 0 && !handle.lastUsed.IsZero() && handle.lastUsed.Before(staleCutoff) {
			toClose = append(toClose, handle)
			delete(mount.handles, fh)
		}
	}

	if fsrpcMaxHandleEntries > 0 && len(mount.handles) > fsrpcMaxHandleEntries {
		type candidate struct {
			fh       uint64
			handle   *fsrpcHandle
			lastUsed time.Time
		}
		candidates := make([]candidate, 0, len(mount.handles))
		for fh, handle := range mount.handles {
			if handle == nil || handle.file == nil {
				delete(mount.handles, fh)
				continue
			}
			candidates = append(candidates, candidate{fh: fh, handle: handle, lastUsed: handle.lastUsed})
		}
		sort.Slice(candidates, func(i, j int) bool {
			return candidates[i].lastUsed.Before(candidates[j].lastUsed)
		})
		extra := len(mount.handles) - fsrpcMaxHandleEntries
		for i := 0; i < extra && i < len(candidates); i++ {
			candidate := candidates[i]
			if _, exists := mount.handles[candidate.fh]; !exists {
				continue
			}
			toClose = append(toClose, candidate.handle)
			delete(mount.handles, candidate.fh)
		}
	}

	return toClose
}

func (mount *fsrpcMount) gcInodesLocked() {
	if fsrpcMaxInodeEntries <= 0 || len(mount.pathToIno) <= fsrpcMaxInodeEntries {
		return
	}

	pinned := map[uint64]struct{}{1: {}}
	for _, handle := range mount.handles {
		if handle == nil || handle.ino == 0 {
			continue
		}
		pinned[handle.ino] = struct{}{}
	}

	for path, ino := range mount.pathToIno {
		if len(mount.pathToIno) <= fsrpcMaxInodeEntries {
			break
		}
		if _, keep := pinned[ino]; keep {
			continue
		}
		if !isWithinRoot(mount.root, path) {
			mount.removePathMappingLocked(path)
			continue
		}
		if _, err := os.Lstat(path); err != nil {
			if os.IsNotExist(err) {
				mount.removePathMappingLocked(path)
			}
		}
	}
}

func (mount *fsrpcMount) removePathMapping(path string) {
	clean := filepath.Clean(path)
	mount.mu.Lock()
	defer mount.mu.Unlock()
	mount.removePathMappingLocked(clean)
}

func (mount *fsrpcMount) removePathMappingLocked(clean string) {
	ino, ok := mount.pathToIno[clean]
	if !ok {
		return
	}
	delete(mount.pathToIno, clean)
	if existingPath, ok := mount.inoToPath[ino]; ok && existingPath == clean {
		delete(mount.inoToPath, ino)
	}
}

func (mount *fsrpcMount) remapPath(oldPath, newPath string) {
	oldPath = filepath.Clean(oldPath)
	newPath = filepath.Clean(newPath)

	mount.mu.Lock()
	defer mount.mu.Unlock()

	updates := make(map[string]uint64)
	prefix := oldPath + string(os.PathSeparator)
	for path, ino := range mount.pathToIno {
		if path == oldPath {
			updates[newPath] = ino
			continue
		}
		if strings.HasPrefix(path, prefix) {
			suffix := strings.TrimPrefix(path, oldPath)
			updates[newPath+suffix] = ino
		}
	}

	for path := range updates {
		mount.removePathMappingLocked(path)
	}
	for path := range mount.pathToIno {
		if path == oldPath || strings.HasPrefix(path, prefix) {
			mount.removePathMappingLocked(path)
		}
	}
	for path, ino := range updates {
		mount.pathToIno[path] = ino
		mount.inoToPath[ino] = path
	}
}

func (mount *fsrpcMount) resolveChildPath(parentPath, name string) (string, int32, string) {
	if name == "" {
		return "", linuxErrInval, "empty name"
	}
	if strings.Contains(name, string(os.PathSeparator)) {
		return "", linuxErrInval, "path separators are not allowed"
	}

	target := parentPath
	switch name {
	case ".":
		target = parentPath
	case "..":
		if parentPath == mount.root {
			target = mount.root
		} else {
			target = filepath.Dir(parentPath)
		}
	default:
		target = filepath.Join(parentPath, name)
	}
	target = filepath.Clean(target)

	if !isWithinRoot(mount.root, target) {
		return "", linuxErrAcces, "path escapes mount root"
	}

	return target, 0, ""
}

func isWithinRoot(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != ".."
}

func (mount *fsrpcMount) buildAttr(ino uint64, info os.FileInfo) map[string]interface{} {
	mod := info.ModTime().UnixMilli()
	if mod < 0 {
		mod = 0
	}

	mode := fuseModeFromFileMode(info.Mode())
	size := info.Size()
	if size < 0 {
		size = 0
	}
	blocks := uint64((size + 511) / 512)
	nlink := uint64(1)
	if info.IsDir() {
		nlink = 2
	}

	return map[string]interface{}{
		"ino":      ino,
		"size":     uint64(size),
		"blocks":   blocks,
		"atime_ms": uint64(mod),
		"mtime_ms": uint64(mod),
		"ctime_ms": uint64(mod),
		"mode":     mode,
		"nlink":    nlink,
		"uid":      uint64(0),
		"gid":      uint64(0),
		"rdev":     uint64(0),
		"blksize":  uint64(4096),
	}
}

func fuseModeFromFileMode(mode os.FileMode) uint64 {
	perm := uint64(mode.Perm())
	switch {
	case mode.IsDir():
		return uint64(0o040000) | perm
	case mode&os.ModeSymlink != 0:
		return uint64(0o120000) | perm
	default:
		return uint64(0o100000) | perm
	}
}

func dtypeFromFileMode(mode os.FileMode) uint32 {
	switch {
	case mode.IsDir():
		return linuxDTDir
	case mode&os.ModeSymlink != 0:
		return linuxDTLnk
	case mode.IsRegular() || mode == 0:
		return linuxDTReg
	default:
		return linuxDTUnknown
	}
}

func hasWriteIntent(flags uint64) bool {
	if flags&linuxOAccMode != 0 {
		return true
	}
	if flags&linuxOCreat != 0 {
		return true
	}
	if flags&linuxOTrunc != 0 {
		return true
	}
	if flags&linuxOAppend != 0 {
		return true
	}
	return false
}

func getReqUint(req map[string]interface{}, key string) (uint64, bool) {
	value, ok := req[key]
	if !ok {
		return 0, false
	}
	out, ok := toUint64(value)
	return out, ok
}

func getReqString(req map[string]interface{}, key string) (string, bool) {
	value, ok := req[key]
	if !ok {
		return "", false
	}
	str, ok := value.(string)
	if !ok {
		return "", false
	}
	return str, true
}

func getReqBytes(req map[string]interface{}, key string) ([]byte, bool) {
	value, ok := req[key]
	if !ok {
		return nil, false
	}
	bytes, ok := value.([]byte)
	if !ok {
		return nil, false
	}
	return bytes, true
}

func toUint64(value interface{}) (uint64, bool) {
	switch v := value.(type) {
	case uint64:
		return v, true
	case uint32:
		return uint64(v), true
	case uint16:
		return uint64(v), true
	case uint8:
		return uint64(v), true
	case int64:
		if v < 0 {
			return 0, false
		}
		return uint64(v), true
	case int:
		if v < 0 {
			return 0, false
		}
		return uint64(v), true
	default:
		return 0, false
	}
}

func osOpenFlagsFromLinux(flags uint64) int {
	openFlags := 0
	switch flags & linuxOAccMode {
	case linuxOWrOnly:
		openFlags |= os.O_WRONLY
	case linuxORDWR:
		openFlags |= os.O_RDWR
	}
	if flags&linuxOCreat != 0 {
		openFlags |= os.O_CREATE
	}
	if flags&linuxOExcl != 0 {
		openFlags |= os.O_EXCL
	}
	if flags&linuxOTrunc != 0 {
		openFlags |= os.O_TRUNC
	}
	if flags&linuxOAppend != 0 {
		openFlags |= os.O_APPEND
	}
	return openFlags
}

func mapOSError(err error) int32 {
	if err == nil {
		return 0
	}

	if os.IsNotExist(err) {
		return linuxErrNoent
	}
	if os.IsPermission(err) {
		return linuxErrAcces
	}
	if os.IsExist(err) {
		return linuxErrExist
	}
	if errors.Is(err, os.ErrClosed) {
		return linuxErrBadf
	}

	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		err = pathErr.Err
	}
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		err = linkErr.Err
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch int(errno) {
		case 2: // ENOENT
			return linuxErrNoent
		case 1, 13: // EPERM, EACCES
			return linuxErrAcces
		case 17: // EEXIST
			return linuxErrExist
		case 20: // ENOTDIR
			return linuxErrNotDir
		case 21: // EISDIR
			return linuxErrIsDir
		case 22: // EINVAL
			return linuxErrInval
		case 30: // EROFS
			return linuxErrROFS
		case 40: // ELOOP
			return linuxErrLoop
		case 9: // EBADF
			return linuxErrBadf
		}
	}

	return linuxErrIO
}

func (mount *fsrpcMount) modeLabel() string {
	if mount.readOnly() {
		return "ro"
	}
	return "rw"
}

func (mount *fsrpcMount) readOnly() bool {
	mode := mount.mode
	if mode == klitkav1.MountMode_MOUNT_MODE_UNSPECIFIED {
		mode = klitkav1.MountMode_MOUNT_MODE_RO
	}
	return mode == klitkav1.MountMode_MOUNT_MODE_RO
}

func startFSRPCServers(mounts []vmMount, timeout time.Duration) ([]*fsrpcServer, error) {
	servers := make([]*fsrpcServer, 0)
	cleanup := func() {
		for _, server := range servers {
			_ = server.Close()
		}
	}

	for _, mount := range mounts {
		if !mount.rpc {
			continue
		}

		conn, err := dialUnixSocket(mount.socketPath, timeout)
		if err != nil {
			cleanup()
			return nil, err
		}

		server := newFSRPCServer(conn, newFSRPCMount(mount.hostPath, mount.mode))
		servers = append(servers, server)
		go func(s *fsrpcServer) {
			if err := s.serve(); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, io.EOF) {
				log.Printf("fsrpc status=server_error mount=%q err=%q", s.mount.root, err.Error())
			}
		}(server)
	}

	return servers, nil
}

func dialUnixSocket(path string, timeout time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.Dial("unix", path)
		if err == nil {
			return conn, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("dial unix socket %s: %w", path, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
