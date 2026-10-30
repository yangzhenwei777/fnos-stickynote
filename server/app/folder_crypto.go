package app

import (
	"stickynote/changelog"
	"stickynote/database"
	"stickynote/model"

	"golang.org/x/crypto/bcrypt"
)

// folder_crypto.go —— v2.3.0 文件夹访问锁服务。
//
// 只做「密码校验 + 锁定态持久化」，不触碰笔记正文：文件夹加密与单篇笔记加密完全正交。
// 服务端模式（NewServerFolderService）事务内 version++ + FOLDER 事件；
// 桌面模式（NewFolderService）只改本地行，由 LocalService 入 Outbox 同步。

func (s *FolderService) Encrypt(id, userID int64, password, confirmPassword string) (int64, error) {
	if password == "" {
		return 0, ErrPasswordInvalid
	}
	if password != confirmPassword {
		return 0, ErrPasswordMismatch
	}
	if err := validateEncryptPassword(password); err != nil {
		return 0, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return 0, err
	}

	tx, err := database.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if err := model.SetFolderEncryptedTx(tx, id, userID, string(hash)); err != nil {
		return 0, err
	}
	version := int64(0)
	if s.serverEvents {
		version, err = bumpFolderVersionTx(tx, id)
		if err != nil {
			return 0, err
		}
		if _, err := changelog.AppendFolderChange(tx, userID, id, "UPDATE", ""); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if !s.serverEvents {
		if f, ferr := model.GetFolder(id, userID); ferr == nil {
			version = f.Version
		}
	}
	return version, nil
}

func (s *FolderService) Decrypt(id, userID int64, password string) (int64, error) {
	_, isEncrypted, hash, version, err := model.GetFolderEncryptInfo(id, userID)
	if err != nil {
		return 0, err
	}
	if !isEncrypted {
		return 0, model.ErrFolderNotEncrypted
	}
	if err := s.verifyPassword(hash, password); err != nil {
		return 0, err
	}

	tx, err := database.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if err := model.ClearFolderEncryptedTx(tx, id, userID); err != nil {
		return 0, err
	}
	if s.serverEvents {
		version, err = bumpFolderVersionTx(tx, id)
		if err != nil {
			return 0, err
		}
		if _, err := changelog.AppendFolderChange(tx, userID, id, "UPDATE", ""); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if !s.serverEvents {
		if f, ferr := model.GetFolder(id, userID); ferr == nil {
			version = f.Version
		}
	}
	return version, nil
}

// Unlock 校验文件夹密码（只读，不改变锁定态），返回当前哈希供调用方签发授权令牌。
func (s *FolderService) Unlock(id, userID int64, password string) (hash string, version int64, err error) {
	var isEncrypted bool
	_, isEncrypted, hash, version, err = model.GetFolderEncryptInfo(id, userID)
	if err != nil {
		return "", 0, err
	}
	if !isEncrypted {
		return "", 0, model.ErrFolderNotEncrypted
	}
	if err := s.verifyPassword(hash, password); err != nil {
		return "", 0, err
	}
	return hash, version, nil
}

func (s *FolderService) verifyPassword(hash, password string) error {
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return ErrWrongPassword
	}
	return nil
}
