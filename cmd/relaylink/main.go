package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"

	"github.com/gorilla/websocket"

	"github.com/natemellendorf/aethos-relay/internal/model"
)

func main() {
	if len(os.Args) < 3 {
		usage()
	}

	wsURL := os.Args[1]
	u, err := url.Parse(wsURL)
	if err != nil {
		log.Fatalf("invalid websocket url: %v", err)
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		log.Fatalf("unsupported websocket scheme: %s", u.Scheme)
	}

	cmd := os.Args[2]
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		log.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	switch cmd {
	case "hello":
		helloCmd(conn, os.Args[3:])
	case "send":
		sendCmd(conn, os.Args[3:])
	case "pull":
		pullCmd(conn, os.Args[3:])
	case "ack":
		ackCmd(conn, os.Args[3:])
	default:
		usage()
	}
}

func helloCmd(conn *websocket.Conn, args []string) {
	fs := flag.NewFlagSet("hello", flag.ExitOnError)
	wayfarerID := fs.String("wayfarer-id", "", "wayfarer identifier")
	deviceID := fs.String("device-id", "", "device identifier")
	_ = fs.Parse(args)
	if *wayfarerID == "" || *deviceID == "" {
		log.Fatal("--wayfarer-id and --device-id are required")
	}

	frame := model.WSFrame{Type: model.FrameTypeHello, WayfarerID: *wayfarerID, DeviceID: *deviceID}
	writeFrame(conn, frame)
	resp := readFrame(conn)
	printJSON(map[string]any{"request": frame, "response": resp})
}

func sendCmd(conn *websocket.Conn, args []string) {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	wayfarerID := fs.String("wayfarer-id", "", "sender wayfarer identifier")
	deviceID := fs.String("device-id", "", "sender device identifier")
	to := fs.String("to", "", "recipient wayfarer identifier")
	payloadFile := fs.String("payload-file", "", "path to raw payload bytes")
	ttl := fs.Int("ttl", 0, "ttl in seconds")
	_ = fs.Parse(args)

	if *wayfarerID == "" || *deviceID == "" || *to == "" || *payloadFile == "" {
		log.Fatal("--wayfarer-id, --device-id, --to, and --payload-file are required")
	}

	hello := model.WSFrame{Type: model.FrameTypeHello, WayfarerID: *wayfarerID, DeviceID: *deviceID}
	writeFrame(conn, hello)
	_ = readFrame(conn)

	payload, err := os.ReadFile(*payloadFile)
	if err != nil {
		log.Fatalf("read payload file: %v", err)
	}

	frame := model.WSFrame{Type: model.FrameTypeSend, To: *to, TTLSeconds: *ttl, PayloadB64: base64.RawURLEncoding.EncodeToString(payload)}
	writeFrame(conn, frame)
	resp := readFrame(conn)
	printJSON(map[string]any{"request": frame, "response": resp})
}

func pullCmd(conn *websocket.Conn, args []string) {
	fs := flag.NewFlagSet("pull", flag.ExitOnError)
	wayfarerID := fs.String("wayfarer-id", "", "wayfarer identifier")
	deviceID := fs.String("device-id", "", "device identifier")
	limit := fs.Int("limit", 50, "maximum messages")
	outDir := fs.String("out-dir", "", "optional directory to write decoded payloads")
	_ = fs.Parse(args)
	if *wayfarerID == "" || *deviceID == "" {
		log.Fatal("--wayfarer-id and --device-id are required")
	}

	writeFrame(conn, model.WSFrame{Type: model.FrameTypeHello, WayfarerID: *wayfarerID, DeviceID: *deviceID})
	_ = readFrame(conn)

	frame := model.WSFrame{Type: model.FrameTypePull, Limit: *limit}
	writeFrame(conn, frame)
	resp := readFrame(conn)
	if resp.Type != model.FrameTypeMessages {
		printJSON(map[string]any{"request": frame, "response": resp})
		return
	}

	decoded := make([]map[string]any, 0, len(resp.Messages))
	for _, msg := range resp.Messages {
		raw, err := model.DecodePayloadB64(msg.PayloadB64)
		if err != nil {
			log.Fatalf("decode payload for msg %s: %v", msg.MsgID, err)
		}
		entry := map[string]any{"msg_id": msg.MsgID, "from": msg.From, "payload_len": len(raw), "payload_b64": msg.PayloadB64}
		if *outDir != "" {
			path := fmt.Sprintf("%s/%s.bin", *outDir, msg.MsgID)
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				log.Fatalf("write decoded payload: %v", err)
			}
			entry["decoded_file"] = path
		}
		decoded = append(decoded, entry)
	}

	printJSON(map[string]any{"request": frame, "response": map[string]any{"type": resp.Type, "messages": decoded}})
}

func ackCmd(conn *websocket.Conn, args []string) {
	fs := flag.NewFlagSet("ack", flag.ExitOnError)
	wayfarerID := fs.String("wayfarer-id", "", "wayfarer identifier")
	deviceID := fs.String("device-id", "", "device identifier")
	msgID := fs.String("msg-id", "", "message id")
	_ = fs.Parse(args)
	if *wayfarerID == "" || *deviceID == "" || *msgID == "" {
		log.Fatal("--wayfarer-id, --device-id, and --msg-id are required")
	}

	writeFrame(conn, model.WSFrame{Type: model.FrameTypeHello, WayfarerID: *wayfarerID, DeviceID: *deviceID})
	_ = readFrame(conn)

	frame := model.WSFrame{Type: model.FrameTypeAck, MsgID: *msgID}
	writeFrame(conn, frame)
	resp := readFrame(conn)
	printJSON(map[string]any{"request": frame, "response": resp})
}

func writeFrame(conn *websocket.Conn, frame model.WSFrame) {
	if err := conn.WriteJSON(frame); err != nil {
		log.Fatalf("write frame: %v", err)
	}
}

func readFrame(conn *websocket.Conn) model.WSFrame {
	var resp model.WSFrame
	if err := conn.ReadJSON(&resp); err != nil {
		log.Fatalf("read frame: %v", err)
	}
	return resp
}

func printJSON(v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		log.Fatalf("marshal output: %v", err)
	}
	fmt.Println(string(b))
}

func usage() {
	msg := errors.New("usage: relaylink <ws-url> <hello|send|pull|ack> [flags]\n" +
		"hello --wayfarer-id <id> --device-id <id>\n" +
		"send --wayfarer-id <id> --device-id <id> --to <wayfarer_id> --payload-file <path> --ttl <seconds>\n" +
		"pull --wayfarer-id <id> --device-id <id> --limit <N> [--out-dir <dir>]\n" +
		"ack --wayfarer-id <id> --device-id <id> --msg-id <uuid>")
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(2)
}
