package app

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"stickynote/model"
)

type webErrorContract struct {
	SchemaVersion int `json:"schemaVersion"`
	Cases         []struct {
		Name     string `json:"name"`
		Domain   string `json:"domain"`
		Sentinel string `json:"sentinel"`
		GoError  string `json:"goError"`
		AppCode  string `json:"appCode"`
	} `json:"cases"`
}

func contractSentinel(name string) error {
	switch name {
	case "not_found":
		return ErrNotFound
	case "wrong_password":
		return ErrWrongPassword
	case "quota_exceeded":
		return ErrQuotaExceeded
	case "version_conflict":
		return ErrVersionConflict
	case "folder_name_exists":
		return model.ErrFolderNameExists
	case "folder_immutable":
		return model.ErrFolderDefault
	case "folder_locked":
		return model.ErrFolderLocked
	default:
		return errors.New("unknown contract sentinel: " + name)
	}
}

// Web 与 Wails localTransport 消费同一份 fixture，防止双端错误映射独立漂移。
func TestWebErrorContract(t *testing.T) {
	raw, err := os.ReadFile("../../web/src/__tests__/fixtures/error-contract.json")
	if err != nil {
		t.Fatalf("read shared error contract: %v", err)
	}
	var contract webErrorContract
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatalf("parse shared error contract: %v", err)
	}
	if contract.SchemaVersion != 1 || len(contract.Cases) == 0 {
		t.Fatalf("invalid shared error contract: version=%d cases=%d", contract.SchemaVersion, len(contract.Cases))
	}
	for _, tc := range contract.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			sentinel := contractSentinel(tc.Sentinel)
			if sentinel.Error() != tc.GoError {
				t.Fatalf("Go sentinel = %q, fixture = %q", sentinel.Error(), tc.GoError)
			}
			code := ErrorCodeFor(sentinel)
			if tc.Domain == "folder" {
				code = ErrorCodeForFolder(sentinel)
			}
			if code != tc.AppCode {
				t.Fatalf("code = %q, want %q", code, tc.AppCode)
			}
		})
	}
}
