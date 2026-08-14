// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/ethercatclient"
)

// ETG.1020 diagnosis history, object 0x10F3:
//
//	:01  maximum messages          :04  new messages available
//	:02  newest message subindex   :05  flags
//	:03  newest acknowledged       :06.. message ring buffer
//
// Each message is DiagCode(u32) Flags(u16) TextId(u16) Timestamp(u64)
// followed by parameters, each a CoE data type index (u16) and its payload.
const (
	diagHistoryIndex    = 0x10f3
	diagSubMaxMessages  = 0x01
	diagSubNewest       = 0x02
	diagSubFirstMessage = 0x06
	diagHeaderSize      = 16
)

// diagMessage is one decoded entry of the diagnosis history.
type diagMessage struct {
	DiagCode  uint32
	Flags     uint16
	TextID    uint16
	Timestamp uint64
	Params    []byte
}

func init() {
	registerCommand(&Command{
		Name:  "diag",
		Brief: "Show the diagnosis history of a slave.",
		Run:   cmdDiag,
	})
}

func cmdDiag(client *ethercatclient.EthercatClient, opts *GlobalOpts, args []string) error {
	if len(args) > 1 {
		return fmt.Errorf("usage: diag [ESIFILE]")
	}

	var texts map[uint16]string
	if len(args) == 1 {
		var err error
		if texts, err = loadEsiDiagTexts(args[0]); err != nil {
			return err
		}
	}

	masterIndex := parseMasterIndex(opts.Masters)
	positions := parsePositionList(opts.Positions)
	if positions == nil {
		positions = []uint16{0}
	}

	for _, pos := range positions {
		if len(positions) > 1 {
			fmt.Printf("=== Slave %d ===\n", pos)
		}
		if err := diagSlave(client, masterIndex, pos, texts); err != nil {
			return err
		}
	}
	return nil
}

func diagSlave(client *ethercatclient.EthercatClient, masterIndex *uint32,
	pos uint16, texts map[uint16]string) error {

	maxMessages, err := diagReadU8(client, masterIndex, pos, diagSubMaxMessages)
	if err != nil {
		return fmt.Errorf("slave %d does not provide a diagnosis history (0x10F3)", pos)
	}
	newest, err := diagReadU8(client, masterIndex, pos, diagSubNewest)
	if err != nil {
		return fmt.Errorf("slave %d does not provide a diagnosis history (0x10F3)", pos)
	}

	// The messages live in a ring buffer, so subindex order is only
	// chronological until it first wraps. Walk it starting at the slot after
	// the newest one, which is the oldest once wrapped and empty before that.
	// Timestamps cannot be used to order these: they are the slave's local
	// time since power-on and restart from zero across a power cycle.
	count := int(maxMessages)
	if diagSubFirstMessage+count-1 > 0xff {
		count = 0x100 - diagSubFirstMessage
	}
	start := 0
	if int(newest) >= diagSubFirstMessage && int(newest) < diagSubFirstMessage+count {
		start = int(newest) - diagSubFirstMessage + 1
	}

	shown := 0
	for i := 0; i < count; i++ {
		sub := diagSubFirstMessage + (start+i)%count
		msg, ok := diagReadMessage(client, masterIndex, pos, uint8(sub))
		if !ok {
			continue
		}

		marker := " "
		if uint8(sub) == newest {
			marker = "*"
		}
		line := fmt.Sprintf("%s [%9.2fs] %-7s code 0x%08x text 0x%04x",
			marker, float64(msg.Timestamp)/1e9, diagTypeName(msg.Flags),
			msg.DiagCode, msg.TextID)
		if text, found := texts[msg.TextID]; found {
			line += "  " + text
		}
		if params := diagFormatParams(msg.Params); params != "" {
			line += "  (" + params + ")"
		}
		fmt.Println(line)
		shown++
	}

	if shown == 0 {
		fmt.Println("Diagnosis history is empty.")
	} else {
		fmt.Println("(* = newest message)")
	}
	return nil
}

func diagTypeName(flags uint16) string {
	switch flags & 0x000f {
	case 0x00:
		return "Info"
	case 0x01:
		return "Warning"
	case 0x02:
		return "Error"
	default:
		return "?"
	}
}

func diagReadU8(client *ethercatclient.EthercatClient, masterIndex *uint32,
	pos uint16, sub uint8) (uint8, error) {

	result, err := client.SdoUpload(masterIndex, pos, diagHistoryIndex, sub, nil)
	if err != nil {
		return 0, err
	}
	if result.AbortCode != 0 {
		return 0, fmt.Errorf("SDO abort code 0x%08x", result.AbortCode)
	}
	if len(result.Data) < 1 {
		return 0, fmt.Errorf("short read")
	}
	return result.Data[0], nil
}

// diagReadMessage reads one message slot. An unused slot may abort or read back
// all zeroes; both mean "nothing here", not an error.
func diagReadMessage(client *ethercatclient.EthercatClient, masterIndex *uint32,
	pos uint16, sub uint8) (diagMessage, bool) {

	var msg diagMessage

	result, err := client.SdoUpload(masterIndex, pos, diagHistoryIndex, sub, nil)
	if err != nil || result.AbortCode != 0 || len(result.Data) < diagHeaderSize {
		return msg, false
	}

	data := result.Data
	msg.DiagCode = binary.LittleEndian.Uint32(data[0:4])
	msg.Flags = binary.LittleEndian.Uint16(data[4:6])
	msg.TextID = binary.LittleEndian.Uint16(data[6:8])
	msg.Timestamp = binary.LittleEndian.Uint64(data[8:16])
	msg.Params = data[diagHeaderSize:]

	if msg.DiagCode == 0 && msg.Flags == 0 && msg.TextID == 0 && msg.Timestamp == 0 {
		return msg, false
	}
	return msg, true
}

// diagFormatParams decodes the parameter block. Each parameter is a CoE data
// type index followed by its payload. Only the fixed-width numeric types are
// decoded; anything else stops the decode and the remainder is dumped as hex,
// so an unknown encoding cannot be misreported as a value.
func diagFormatParams(params []byte) string {
	var out []string
	pos := 0

	for pos+2 <= len(params) {
		typ := binary.LittleEndian.Uint16(params[pos : pos+2])
		var size int
		var signed bool
		switch typ {
		case 0x0002:
			size, signed = 1, true // INTEGER8
		case 0x0003:
			size, signed = 2, true // INTEGER16
		case 0x0004:
			size, signed = 4, true // INTEGER32
		case 0x0005:
			size, signed = 1, false // UNSIGNED8
		case 0x0006:
			size, signed = 2, false // UNSIGNED16
		case 0x0007:
			size, signed = 4, false // UNSIGNED32
		default:
			size = 0
		}
		if size == 0 || pos+2+size > len(params) {
			break
		}
		pos += 2

		var raw uint32
		switch size {
		case 1:
			raw = uint32(params[pos])
		case 2:
			raw = uint32(binary.LittleEndian.Uint16(params[pos : pos+2]))
		default:
			raw = binary.LittleEndian.Uint32(params[pos : pos+4])
		}
		if signed {
			switch size {
			case 1:
				out = append(out, strconv.Itoa(int(int8(raw))))
			case 2:
				out = append(out, strconv.Itoa(int(int16(raw))))
			default:
				out = append(out, strconv.Itoa(int(int32(raw))))
			}
		} else {
			out = append(out, strconv.FormatUint(uint64(raw), 10))
		}
		pos += size
	}

	// anything not understood is shown verbatim rather than guessed at
	if rest := params[pos:]; len(rest) > 0 {
		trailing := false
		for _, b := range rest {
			if b != 0 {
				trailing = true
				break
			}
		}
		if trailing {
			var hex strings.Builder
			hex.WriteString("raw")
			for _, b := range rest {
				fmt.Fprintf(&hex, " %02x", b)
			}
			out = append(out, hex.String())
		}
	}

	return strings.Join(out, ", ")
}

// loadEsiDiagTexts scans an ESI file for <DiagMessage> text IDs and their
// English texts. Deliberately a plain text scan rather than an XML parse, to
// match the behaviour of the C++ tool's 'diag' command.
func loadEsiDiagTexts(path string) (map[uint16]string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open ESI file '%s': %v", path, err)
	}

	texts := make(map[uint16]string)
	s := string(content)
	pos := 0

	for {
		start := strings.Index(s[pos:], "<DiagMessage>")
		if start < 0 {
			break
		}
		start += pos
		end := strings.Index(s[start:], "</DiagMessage>")
		if end < 0 {
			break
		}
		end += start
		block := s[start:end]
		pos = end

		idStart := strings.Index(block, "<TextId>")
		if idStart < 0 {
			continue
		}
		idStart += len("<TextId>")
		idEnd := strings.Index(block[idStart:], "</TextId>")
		if idEnd < 0 {
			continue
		}
		idStr := block[idStart : idStart+idEnd]
		// device descriptions write these as "#x3421"
		if len(idStr) > 2 && idStr[0] == '#' && (idStr[1]|0x20) == 'x' {
			idStr = "0x" + idStr[2:]
		}
		id, err := strconv.ParseUint(idStr, 0, 16)
		if err != nil {
			continue
		}

		// prefer the English text, fall back to the first one present
		txtStart := strings.Index(block, `<MessageText LcId="1033">`)
		skip := len(`<MessageText LcId="1033">`)
		if txtStart < 0 {
			txtStart = strings.Index(block, "<MessageText")
			if txtStart < 0 {
				continue
			}
			gt := strings.Index(block[txtStart:], ">")
			if gt < 0 {
				continue
			}
			txtStart += gt
			skip = 1
		}
		txtStart += skip
		txtEnd := strings.Index(block[txtStart:], "</MessageText>")
		if txtEnd < 0 {
			continue
		}
		texts[uint16(id)] = block[txtStart : txtStart+txtEnd]
	}

	return texts, nil
}
