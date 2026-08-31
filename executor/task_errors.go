package executor

import (
	"log"
	"strings"
)

func taskFailure(message string, err error) TaskResult {
	if err == nil {
		return TaskResult{Success: false, Message: message}
	}
	detail := strings.TrimSpace(err.Error())
	if detail == "" {
		return TaskResult{Success: false, Message: message}
	}
	return TaskResult{Success: false, Message: message + ": " + detail}
}

func logRecoveryFailure(action string, err error) {
	if err != nil {
		log.Printf("%s失败: %v", action, err)
	}
}
