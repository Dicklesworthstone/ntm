package context

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Gemini records whole JSON conversations and, in newer versions, append-only
// JSONL journals. The latter are NOT a stream of independent token readings:
// repeated message IDs replace in place, $rewindTo removes an inclusive suffix,
// $set.messages replaces history, and $patch can remove or reorder messages.
// Replay only accounting metadata, never retain prompts or tool results.
// Schema: google-gemini/gemini-cli packages/core/src/services/chatRecordingTypes.ts
// and chatRecordingService.ts (JSONL record accumulator).
const (
	geminiTranscriptMaxBytes    = 64 * 1024 * 1024
	geminiTranscriptMaxLine     = 8 * 1024 * 1024
	geminiTranscriptMaxMessages = 16384
	geminiTranscriptCacheSize   = 32
)

type geminiTokenSummary struct {
	Input    int `json:"input"`
	Output   int `json:"output"`
	Cached   int `json:"cached"`
	Thoughts int `json:"thoughts"`
	Tool     int `json:"tool"`
	Total    int `json:"total"`
}

type geminiUsageMessage struct {
	ID        string              `json:"id"`
	Type      string              `json:"type"`
	Model     string              `json:"model"`
	Timestamp string              `json:"timestamp"`
	Tokens    *geminiTokenSummary `json:"tokens"`
}

type geminiUsageMessages []geminiUsageMessage

// Enforce the collection bound during decoding, including legacy full-history
// checkpoints, rather than allocating an unbounded slice before checking it.
func (messages *geminiUsageMessages) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('[') {
		return errors.New("Gemini messages must be an array")
	}
	var result geminiUsageMessages
	for decoder.More() {
		if len(result) >= geminiTranscriptMaxMessages {
			return errors.New("Gemini transcript exceeds accounting message limit")
		}
		var message geminiUsageMessage
		if err := decoder.Decode(&message); err != nil {
			return err
		}
		result = append(result, message)
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	*messages = result
	return nil
}

type geminiUsageMetadata struct {
	SessionID   string               `json:"sessionId"`
	ProjectHash string               `json:"projectHash"`
	Kind        string               `json:"kind"`
	Messages    *geminiUsageMessages `json:"messages"`
}

type geminiUsageRecord struct {
	geminiUsageMetadata
	geminiUsageMessage
	Rewind *string              `json:"$rewindTo"`
	Set    *geminiUsageMetadata `json:"$set"`
	Patch  *struct {
		RemoveIDs []string `json:"removeIds"`
		OrderIDs  []string `json:"orderIds"`
	} `json:"$patch"`
}

type geminiUsageHistory struct {
	metadata geminiUsageMetadata
	order    []string
	messages map[string]geminiUsageMessage
	nonMain  bool
}

func (h *geminiUsageHistory) put(message geminiUsageMessage) error {
	if message.ID == "" || len(message.ID) > 1024 {
		return errors.New("Gemini transcript has an invalid message ID")
	}
	switch message.Type {
	case "gemini", "user", "info", "error", "warning":
	default:
		return fmt.Errorf("unsupported Gemini message type %q", message.Type)
	}
	if _, exists := h.messages[message.ID]; !exists {
		if len(h.order) >= geminiTranscriptMaxMessages {
			return errors.New("Gemini transcript exceeds accounting message limit")
		}
		h.order = append(h.order, message.ID)
	}
	h.messages[message.ID] = message
	return nil
}

func (h *geminiUsageHistory) replace(messages []geminiUsageMessage) error {
	h.order = nil
	h.messages = make(map[string]geminiUsageMessage)
	for _, message := range messages {
		if err := h.put(message); err != nil {
			return err
		}
	}
	return nil
}

func (h *geminiUsageHistory) apply(record geminiUsageRecord) error {
	controls := 0
	for _, present := range []bool{record.Rewind != nil, record.Set != nil, record.Patch != nil, record.ID != "", record.SessionID != ""} {
		if present {
			controls++
		}
	}
	if controls != 1 {
		return errors.New("unrecognized or ambiguous Gemini transcript record")
	}
	switch {
	case record.Rewind != nil:
		// Gemini clears all history when the target does not exist.
		keep := 0
		for i, id := range h.order {
			if id == *record.Rewind {
				keep = i
				break
			}
		}
		for _, id := range h.order[keep:] {
			delete(h.messages, id)
		}
		h.order = h.order[:keep]
	case record.Patch != nil:
		for _, id := range record.Patch.RemoveIDs {
			delete(h.messages, id)
		}
		// orderIds moves named existing messages to the end, in that order;
		// unnamed messages keep their relative order. Content/tool patches
		// do not change token accounting and are intentionally not retained.
		named := make(map[string]bool, len(record.Patch.OrderIDs))
		var suffix []string
		for _, id := range record.Patch.OrderIDs {
			if _, ok := h.messages[id]; ok && !named[id] {
				named[id] = true
				suffix = append(suffix, id)
			}
		}
		order := make([]string, 0, len(h.order))
		for _, id := range h.order {
			if _, ok := h.messages[id]; ok && !named[id] {
				order = append(order, id)
			}
		}
		h.order = append(order, suffix...)
	case record.Set != nil:
		set := record.Set
		if (set.SessionID != "" && set.SessionID != h.metadata.SessionID) ||
			(set.ProjectHash != "" && set.ProjectHash != h.metadata.ProjectHash) {
			return errors.New("Gemini transcript changes session identity")
		}
		if set.Kind != "" {
			h.metadata.Kind = set.Kind
			h.nonMain = h.nonMain || set.Kind != "main"
		}
		if set.Messages != nil {
			return h.replace(*set.Messages)
		}
	case record.ID != "":
		return h.put(record.geminiUsageMessage)
	default:
		return errors.New("Gemini transcript contains a second session header")
	}
	return nil
}

func (h *geminiUsageHistory) usage() (*TranscriptUsage, error) {
	if h.nonMain || (h.metadata.Kind != "" && h.metadata.Kind != "main") {
		return nil, nil // Subagents must never become a parent pane's context.
	}
	for i := len(h.order) - 1; i >= 0; i-- {
		message := h.messages[h.order[i]]
		if message.Type != "gemini" || message.Tokens == nil {
			continue
		}
		tokens := message.Tokens
		maxInt := int(^uint(0) >> 1)
		total := 0
		for _, value := range []int{tokens.Input, tokens.Output, tokens.Thoughts, tokens.Tool} {
			if value < 0 || value > maxInt-total {
				return nil, errors.New("invalid Gemini transcript token accounting")
			}
			total += value
		}
		if tokens.Cached < 0 || tokens.Cached > tokens.Input || tokens.Total < 0 {
			return nil, errors.New("invalid Gemini transcript cached/total tokens")
		}
		if tokens.Total > 0 {
			// cachedContentTokenCount is a subset of promptTokenCount, not
			// extra occupancy. Native total includes thoughts/tool tokens.
			if tokens.Total < tokens.Input || tokens.Total < tokens.Output {
				return nil, errors.New("inconsistent Gemini transcript total tokens")
			}
			total = tokens.Total
		}
		if total == 0 {
			continue // An aborted/empty response is not a zero-context reading.
		}
		updatedAt, err := time.Parse(time.RFC3339Nano, message.Timestamp)
		if err != nil {
			return nil, fmt.Errorf("Gemini usage timestamp: %w", err)
		}
		return &TranscriptUsage{
			ProviderSessionID: h.metadata.SessionID,
			Model:             message.Model,
			Tokens:            total,
			InputTokens:       tokens.Input - tokens.Cached,
			CacheReadTokens:   tokens.Cached,
			OutputTokens:      tokens.Output,
			UpdatedAt:         updatedAt,
		}, nil
	}
	return nil, nil
}

func parseGeminiTranscriptUsage(src io.Reader, jsonl bool) (*TranscriptUsage, error) {
	history := geminiUsageHistory{messages: make(map[string]geminiUsageMessage)}
	if !jsonl {
		decoder := json.NewDecoder(src)
		if err := decoder.Decode(&history.metadata); err != nil {
			return nil, fmt.Errorf("decode Gemini conversation: %w", err)
		}
		var trailing json.RawMessage
		if err := decoder.Decode(&trailing); err != io.EOF {
			return nil, errors.New("Gemini conversation has trailing or incomplete data")
		}
		if history.metadata.SessionID == "" || history.metadata.ProjectHash == "" || history.metadata.Messages == nil {
			return nil, errors.New("Gemini conversation metadata is incomplete")
		}
		if err := history.replace(*history.metadata.Messages); err != nil {
			return nil, err
		}
	} else {
		scanner := bufio.NewScanner(src)
		scanner.Buffer(make([]byte, 64*1024), geminiTranscriptMaxLine)
		headerSeen := false
		for scanner.Scan() {
			line := bytes.TrimSpace(scanner.Bytes())
			if len(line) == 0 {
				continue
			}
			var record geminiUsageRecord
			if err := json.Unmarshal(line, &record); err != nil {
				// A partial record could be a rewind or a replacement: never
				// publish an earlier high-confidence count past that barrier.
				return nil, fmt.Errorf("decode Gemini journal: %w", err)
			}
			if !headerSeen {
				if record.SessionID == "" || record.ProjectHash == "" || record.ID != "" || record.Rewind != nil || record.Set != nil || record.Patch != nil {
					return nil, errors.New("Gemini journal metadata is incomplete")
				}
				history.metadata = record.geminiUsageMetadata
				history.nonMain = record.Kind != "" && record.Kind != "main"
				if record.Messages != nil {
					if err := history.replace(*record.Messages); err != nil {
						return nil, err
					}
				}
				headerSeen = true
				continue
			}
			if err := history.apply(record); err != nil {
				return nil, err
			}
		}
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("read Gemini journal: %w", err)
		}
		if !headerSeen {
			return nil, errors.New("Gemini journal is empty")
		}
	}
	return history.usage()
}

// Unchanged native transcripts are common on repeated status/rotation polls.
// Cache only a bounded accounting projection by inode, size and mtime. A rewrite,
// truncation or replacement triggers full replay, not unsafe append assumptions.
var geminiUsageCache = struct {
	sync.Mutex
	clock   uint64
	entries map[string]geminiUsageCacheEntry
}{entries: make(map[string]geminiUsageCacheEntry)}

type geminiUsageCacheEntry struct {
	info  os.FileInfo
	usage *TranscriptUsage
	used  uint64
}

func cloneGeminiTranscriptUsage(usage *TranscriptUsage) *TranscriptUsage {
	if usage == nil {
		return nil
	}
	copy := *usage
	return &copy
}

// readGeminiTranscriptUsage identifies the provider format without changing f's
// seek position. A recognized but unreadable journal fails closed; it is never
// interpreted as Claude/Codex JSONL. Every read is bounded to one file snapshot.
func readGeminiTranscriptUsage(f *os.File, info os.FileInfo, path string) (*TranscriptUsage, bool, error) {
	key, err := filepath.Abs(path)
	if err != nil {
		return nil, false, err
	}
	geminiUsageCache.Lock()
	cached, ok := geminiUsageCache.entries[key]
	if ok && os.SameFile(cached.info, info) && cached.info.Size() == info.Size() && cached.info.ModTime().Equal(info.ModTime()) {
		geminiUsageCache.clock++
		cached.used = geminiUsageCache.clock
		geminiUsageCache.entries[key] = cached
		usage := cloneGeminiTranscriptUsage(cached.usage)
		if usage != nil {
			usage.Path = path
		}
		geminiUsageCache.Unlock()
		return usage, true, nil
	}
	geminiUsageCache.Unlock()

	jsonl := strings.EqualFold(filepath.Ext(path), ".jsonl")
	if !jsonl && !strings.EqualFold(filepath.Ext(path), ".json") {
		return nil, false, nil
	}
	if jsonl {
		// Other providers need no additional JSON decode or whole-file scan.
		// Native Gemini writes both identifiers in the first metadata record.
		head := make([]byte, 4096)
		n, readErr := f.ReadAt(head, 0)
		if readErr != nil && readErr != io.EOF {
			return nil, false, readErr
		}
		head = head[:n]
		if nl := bytes.IndexByte(head, '\n'); nl >= 0 {
			head = head[:nl]
		}
		if !bytes.Contains(head, []byte(`"sessionId"`)) || !bytes.Contains(head, []byte(`"projectHash"`)) {
			return nil, false, nil
		}
	}
	if !info.Mode().IsRegular() || info.Size() > geminiTranscriptMaxBytes {
		return nil, true, errors.New("Gemini transcript is not a bounded regular file")
	}
	usage, err := parseGeminiTranscriptUsage(io.NewSectionReader(f, 0, info.Size()), jsonl)
	if err != nil {
		return nil, true, err
	}
	after, err := f.Stat()
	if err != nil {
		return nil, true, err
	}
	if after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return nil, true, errors.New("Gemini transcript changed during accounting read")
	}
	if usage != nil {
		usage.Path = path
		// Metadata-only writes must not make an old usage record look fresh.
		// Also do not claim evidence newer than the snapshot we actually read.
		if usage.UpdatedAt.After(info.ModTime()) {
			usage.UpdatedAt = info.ModTime()
		}
	}
	geminiUsageCache.Lock()
	defer geminiUsageCache.Unlock()
	if len(geminiUsageCache.entries) >= geminiTranscriptCacheSize {
		oldest, oldestUse := "", ^uint64(0)
		for path, entry := range geminiUsageCache.entries {
			if entry.used < oldestUse {
				oldest, oldestUse = path, entry.used
			}
		}
		delete(geminiUsageCache.entries, oldest)
	}
	geminiUsageCache.clock++
	geminiUsageCache.entries[key] = geminiUsageCacheEntry{info: info, usage: cloneGeminiTranscriptUsage(usage), used: geminiUsageCache.clock}
	return usage, true, nil
}
