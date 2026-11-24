package cmd

import (
	"embed"
	"strings"

	"github.com/ForceCLI/warden-plugin/core"
)

// The message files are those of the warden sf plugin: a "# key" line
// followed by the text of the message.
//
//go:embed messages/*.md
var messageFiles embed.FS

// Messages are the texts of one command.
type Messages struct {
	texts map[string]string
}

var loadedMessages = map[string]*Messages{}

// loadMessages returns the messages of a bundle such as "warden.freeze".
func loadMessages(bundle string) *Messages {
	if cached, ok := loadedMessages[bundle]; ok {
		return cached
	}
	content, err := messageFiles.ReadFile("messages/" + bundle + ".md")
	if err != nil {
		panic("missing message bundle " + bundle)
	}
	messages := &Messages{texts: map[string]string{}}
	key := ""
	var lines []string
	flush := func() {
		if key != "" {
			messages.texts[key] = strings.TrimSpace(strings.Join(lines, "\n"))
		}
		lines = nil
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n") {
		if name, ok := strings.CutPrefix(line, "# "); ok {
			flush()
			key = strings.TrimSpace(name)
			continue
		}
		lines = append(lines, line)
	}
	flush()
	loadedMessages[bundle] = messages
	return messages
}

// Get returns the text for a key with each %s replaced by the next
// argument.  An unknown key is returned as it is.
func (m *Messages) Get(key string, args ...string) string {
	template, ok := m.texts[key]
	if !ok {
		return key
	}
	return core.Format(template, args...)
}

// Has reports whether the bundle has a key.
func (m *Messages) Has(key string) bool {
	_, ok := m.texts[key]
	return ok
}

// List returns the items of a message written as a list, one "- " item
// for each entry, with the continuation lines of an item joined to it.
func (m *Messages) List(key string) []string {
	var items []string
	for _, line := range strings.Split(m.texts[key], "\n") {
		if item, ok := strings.CutPrefix(line, "- "); ok {
			items = append(items, item)
		} else if len(items) > 0 && strings.TrimSpace(line) != "" {
			items[len(items)-1] += "\n" + line
		}
	}
	return items
}
