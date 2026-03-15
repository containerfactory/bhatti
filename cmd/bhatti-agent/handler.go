//go:build linux

package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"

	"github.com/sahilshubham/bhatti/pkg/agent/proto"
)

func handleControlConnection(conn net.Conn) {
	defer conn.Close()

	msgType, payload, err := proto.ReadFrame(conn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bhatti-agent: control read: %v\n", err)
		return
	}

	if msgType != proto.EXEC_REQ {
		proto.WriteFrame(conn, proto.ERROR, []byte(fmt.Sprintf("expected EXEC_REQ, got 0x%02x", msgType)))
		return
	}

	var req proto.ExecRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		proto.WriteFrame(conn, proto.ERROR, []byte(fmt.Sprintf("bad exec request: %v", err)))
		return
	}

	if len(req.Argv) == 0 {
		proto.WriteFrame(conn, proto.ERROR, []byte("empty argv"))
		return
	}

	if req.TTY != nil && *req.TTY {
		handleTTYExec(conn, req)
	} else {
		handlePipedExec(conn, req)
	}
}
