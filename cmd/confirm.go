package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// stdin is where confirmations are read from.  Tests replace it.
var stdin io.Reader = os.Stdin

// confirmTimeout is how long a confirmation waits for an answer.
var confirmTimeout = 10 * time.Second

// confirm asks a yes/no question on stderr and reads the answer from
// stdin.  It returns false for any answer but y or yes, for the end of the
// input, and when no answer arrives within confirmTimeout.
func confirm(errOut io.Writer, message string) bool {
	fmt.Fprintf(errOut, "%s (y/n) ", message)
	answers := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdin).ReadString('\n')
		answers <- line
	}()
	select {
	case answer := <-answers:
		answer = strings.ToLower(strings.TrimSpace(answer))
		return answer == "y" || answer == "yes"
	case <-time.After(confirmTimeout):
		fmt.Fprintln(errOut)
		return false
	}
}

// confirmLifecycleWrites asks before a command changes the org.  A
// declined or unanswered question ends the command with the
// errorPromptDeclined text of its messages.
func confirmLifecycleWrites(run *invocation, required bool, messages *Messages) error {
	if !required {
		return nil
	}
	if confirm(run.errOut, messages.Get("promptContinue")) {
		return nil
	}
	run.warn(messages.Get("warningPromptTimeout"))
	return sfError(messages.Get("errorPromptDeclined"))
}
