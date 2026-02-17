//go:build !windows

package tests

import (
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"testing"
)

func startTestDNSServer(t *testing.T, records map[string]string) (string, <-chan string) {
	t.Helper()

	normalized := make(map[string]net.IP, len(records))
	for name, value := range records {
		ip := net.ParseIP(strings.TrimSpace(value)).To4()
		if ip == nil {
			t.Fatalf("invalid IPv4 record for %s: %q", name, value)
		}
		normalized[strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")] = ip
	}

	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start dns server: %v", err)
	}
	queries := make(chan string, 32)

	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, readErr := conn.ReadFrom(buf)
			if readErr != nil {
				return
			}
			response, query := buildDNSResponse(buf[:n], normalized)
			if query != "" {
				select {
				case queries <- query:
				default:
				}
			}
			if len(response) > 0 {
				_, _ = conn.WriteTo(response, addr)
			}
		}
	}()

	t.Cleanup(func() {
		_ = conn.Close()
	})

	return conn.LocalAddr().String(), queries
}

func buildDNSResponse(msg []byte, records map[string]net.IP) ([]byte, string) {
	name, qtype, qclass, questionEnd, err := parseDNSQuestion(msg)
	if err != nil {
		return nil, ""
	}
	if qclass != 1 {
		return dnsHeader(msg, 0x8180, 1, 0), name
	}

	question := msg[12:questionEnd]
	ip, found := records[name]

	// AAAA queries get a valid NOERROR empty answer to allow fallback to A.
	if qtype == 28 {
		resp := dnsHeader(msg, 0x8180, 1, 0)
		resp = append(resp, question...)
		return resp, name
	}

	if qtype != 1 {
		resp := dnsHeader(msg, 0x8180, 1, 0)
		resp = append(resp, question...)
		return resp, name
	}
	if !found {
		resp := dnsHeader(msg, 0x8183, 1, 0)
		resp = append(resp, question...)
		return resp, name
	}

	resp := dnsHeader(msg, 0x8180, 1, 1)
	resp = append(resp, question...)
	resp = append(resp,
		0xc0, 0x0c, // pointer to query name
		0x00, 0x01, // type A
		0x00, 0x01, // class IN
		0x00, 0x00, 0x00, 0x3c, // ttl
		0x00, 0x04, // rdlength
	)
	resp = append(resp, ip...)
	return resp, name
}

func parseDNSQuestion(msg []byte) (string, uint16, uint16, int, error) {
	if len(msg) < 12 {
		return "", 0, 0, 0, fmt.Errorf("short dns query")
	}
	qdCount := binary.BigEndian.Uint16(msg[4:6])
	if qdCount == 0 {
		return "", 0, 0, 0, fmt.Errorf("dns query has no questions")
	}

	offset := 12
	labels := []string{}
	for {
		if offset >= len(msg) {
			return "", 0, 0, 0, fmt.Errorf("invalid dns qname")
		}
		labelLen := int(msg[offset])
		offset++
		if labelLen == 0 {
			break
		}
		if (labelLen & 0xc0) != 0 {
			return "", 0, 0, 0, fmt.Errorf("compressed qname not supported")
		}
		if offset+labelLen > len(msg) {
			return "", 0, 0, 0, fmt.Errorf("invalid dns qname label")
		}
		labels = append(labels, strings.ToLower(string(msg[offset:offset+labelLen])))
		offset += labelLen
	}
	if offset+4 > len(msg) {
		return "", 0, 0, 0, fmt.Errorf("short dns question")
	}

	qtype := binary.BigEndian.Uint16(msg[offset : offset+2])
	qclass := binary.BigEndian.Uint16(msg[offset+2 : offset+4])
	offset += 4
	name := strings.Join(labels, ".")
	return name, qtype, qclass, offset, nil
}

func dnsHeader(query []byte, flags uint16, qdCount uint16, anCount uint16) []byte {
	resp := make([]byte, 12)
	copy(resp[:2], query[:2])
	binary.BigEndian.PutUint16(resp[2:4], flags)
	binary.BigEndian.PutUint16(resp[4:6], qdCount)
	binary.BigEndian.PutUint16(resp[6:8], anCount)
	binary.BigEndian.PutUint16(resp[8:10], 0)
	binary.BigEndian.PutUint16(resp[10:12], 0)
	return resp
}
