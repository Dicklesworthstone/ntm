package output

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestFormatterJSON(t *testing.T) {
	buf := &bytes.Buffer{}
	f := New(WithJSON(true))
	f.writer = buf

	data := map[string]string{"hello": "world"}
	if err := f.JSON(data); err != nil {
		t.Fatalf("JSON() error = %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, `"hello"`) {
		t.Errorf("JSON output missing expected content: %s", got)
	}
}

func TestFormatterIsJSON(t *testing.T) {
	tests := []struct {
		opts []Option
		want bool
	}{
		{[]Option{}, false},
		{[]Option{WithJSON(true)}, true},
		{[]Option{WithJSON(false)}, false},
	}

	for _, tt := range tests {
		f := New(tt.opts...)
		if got := f.IsJSON(); got != tt.want {
			t.Errorf("IsJSON() = %v, want %v", got, tt.want)
		}
	}
}

func TestErrorResponse(t *testing.T) {
	err := NewError("something failed")
	if err.Success {
		t.Error("NewError().Success = true, want false")
	}
	if err.Error != "something failed" {
		t.Errorf("NewError().Error = %q, want %q", err.Error, "something failed")
	}
	encoded, marshalErr := json.Marshal(err)
	if marshalErr != nil {
		t.Fatalf("json.Marshal(NewError()) error = %v", marshalErr)
	}
	if !bytes.Contains(encoded, []byte(`"success":false`)) {
		t.Fatalf("json.Marshal(NewError()) = %s, want explicit success:false", encoded)
	}

	err = NewErrorWithCode("NOT_FOUND", "session not found")
	if err.Code != "NOT_FOUND" {
		t.Errorf("NewErrorWithCode().Code = %q, want %q", err.Code, "NOT_FOUND")
	}
}

func TestSuccessResponse(t *testing.T) {
	s := NewSuccess("operation completed")
	if !s.Success {
		t.Error("NewSuccess().Success = false, want true")
	}
	if s.Message != "operation completed" {
		t.Errorf("NewSuccess().Message = %q, want %q", s.Message, "operation completed")
	}
}

func TestPluralize(t *testing.T) {
	tests := []struct {
		count    int
		singular string
		plural   string
		want     string
	}{
		{1, "item", "items", "item"},
		{0, "item", "items", "items"},
		{2, "item", "items", "items"},
		{100, "session", "sessions", "sessions"},
	}

	for _, tt := range tests {
		if got := Pluralize(tt.count, tt.singular, tt.plural); got != tt.want {
			t.Errorf("Pluralize(%d, %q, %q) = %q, want %q",
				tt.count, tt.singular, tt.plural, got, tt.want)
		}
	}
}

func TestCountStr(t *testing.T) {
	tests := []struct {
		count    int
		singular string
		plural   string
		want     string
	}{
		{1, "item", "items", "1 item"},
		{5, "item", "items", "5 items"},
	}

	for _, tt := range tests {
		if got := CountStr(tt.count, tt.singular, tt.plural); got != tt.want {
			t.Errorf("CountStr(%d, %q, %q) = %q, want %q",
				tt.count, tt.singular, tt.plural, got, tt.want)
		}
	}
}

func TestTable(t *testing.T) {
	buf := &bytes.Buffer{}
	table := NewTable(buf, "Name", "Value")
	table.AddRow("foo", "bar")
	table.AddRow("hello", "world")
	table.Render()

	got := buf.String()
	if !strings.Contains(got, "Name") {
		t.Error("Table output missing header")
	}
	if !strings.Contains(got, "foo") {
		t.Error("Table output missing row data")
	}
}

func TestFormatterOutputData(t *testing.T) {
	// JSON mode
	buf := &bytes.Buffer{}
	f := New(WithJSON(true))
	f.writer = buf

	jsonData := map[string]string{"test": "value"}
	textCalled := false

	err := f.OutputData(jsonData, func(w io.Writer) error {
		textCalled = true
		return nil
	})

	if err != nil {
		t.Errorf("OutputData returned error: %v", err)
	}
	if textCalled {
		t.Error("Text function called in JSON mode")
	}
	if !strings.Contains(buf.String(), "test") {
		t.Error("JSON output missing expected content")
	}

	// Text mode
	buf.Reset()
	f = New(WithJSON(false))
	f.writer = buf
	textCalled = false

	err = f.OutputData(nil, func(w io.Writer) error {
		textCalled = true
		return nil
	})

	if err != nil {
		t.Errorf("OutputData returned error: %v", err)
	}
	if !textCalled {
		t.Error("Text function not called in text mode")
	}
}
