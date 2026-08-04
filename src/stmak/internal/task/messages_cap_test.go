// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import (
	"fmt"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/emcerror"
)

// The current message list is bounded (drop-oldest): a chatty error source
// must not grow server memory and every UI's notification area without bound
// (AXIS review finding A-17).
func TestMessageListBounded(t *testing.T) {
	task := &Task{}
	total := 3*maxMessageList + 7
	for i := 0; i < total; i++ {
		task.appendMessage(emcerror.ErrorKind_OPERATOR_ERROR, fmt.Sprintf("msg %d", i))
	}
	list := task.messageListSnapshot()
	if len(list) > maxMessageList {
		t.Fatalf("message list grew past the cap: %d > %d", len(list), maxMessageList)
	}
	last := list[len(list)-1]
	if last.Text != fmt.Sprintf("msg %d", total-1) {
		t.Fatalf("newest message lost: %q", last.Text)
	}
	if last.ID != uint64(total) {
		t.Fatalf("IDs must keep monotonic across drops: got %d, want %d", last.ID, total)
	}
}
