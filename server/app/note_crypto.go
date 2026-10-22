package app

import (
	"database/sql"
	"errors"
	"fmt"

	"stickynote/crypto"
	"stickynote/database"
	"stickynote/htmltext"
	"stickynote/model"

	"golang.org/x/crypto/bcrypt"
)

// note_crypto.go — 加密笔记门面（从 handler/note_encrypt.go 提取业务语义，P0-A 接线切片）。
//
// 与服务端逐条对齐的纪律（源码事实 SOURCE-FACT §3）：
//   - 密码 ≤72 字节前置拦截（bcrypt 输入上限，v21.67 P1-01：超长=密文永不可解）
//   - 口令校验哈希 bcrypt.DefaultCost；内容密钥 PBKDF2-600k（NPD1，crypto 包内）
//   - 解密失败与口令错误分流：403 WRONG_PASSWORD vs 500 CORRUPT（不泄露密文可用性）
//   - 全程无明文中间态落库（v21.52 UpdateEncryptedNote 单事务纪律）
//
// P2 批次1·刀2（§60 / SOURCE-FACT #9~#11）：server 模式（NewServerNoteService）下
// Set/Clear/UpdateEncrypted 三件改为事务内 CAS + version++ + UPDATE 事件同提交；
// CAS 基点取事务内当前版本（BEGIN IMMEDIATE 持写锁后读-改-写无竞态），Web API 无需客户端传 version。
// 桌面模式（NewNoteService）保持 P0-A 语义：不产事件、不 bump（桌面加密变更经 Outbox
// push UPDATE 由服务端统一产事件）。
//
// VerifyEncrypt 的防爆破守卫（encryptGuard）留在 handler 侧（HTTP 关注点：IP 维度）；
// 桌面侧对应防线 = 本地解锁（DPAPI，P3），业务语义同一把钥匙。

var (
	ErrWrongPassword    = errors.New("wrong password")
	ErrPasswordMismatch = errors.New("两次输入的密码不一致")
	ErrPasswordTooLong  = errors.New("加密密码过长（最多 72 字节）")
	ErrPasswordInvalid  = errors.New("加密密码无效，请更换后重试")
	ErrCorruptPayload   = errors.New("encrypted payload corrupt")
	// ErrAlreadyEncrypted / ErrNotEncrypted 哨兵已下沉 model（Tx 变体事务内判定），此处别名转发。
	ErrAlreadyEncrypted = model.ErrAlreadyEncrypted
	ErrNotEncrypted     = model.ErrNotEncrypted
)

// EncryptNote 明文笔记 → 加密态。confirmPassword 双入口一致校验（与 Web 表单语义等同，
// 桌面 binding 无独立表单，收敛进服务层防两端漂移）。
// 返回加密后的版本（§60：server 模式 version++；桌面模式为原版本）。
func (s *NoteService) EncryptNote(id, userID int64, password, confirmPassword string) (int64, error) {
	note, err := model.GetNote(id, userID)
	if err != nil {
		return 0, err
	}
	if note.IsEncrypted {
		return 0, ErrAlreadyEncrypted
	}
	if password != confirmPassword {
		return 0, ErrPasswordMismatch
	}
	if err := validateEncryptPassword(password); err != nil {
		return 0, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return 0, ErrPasswordInvalid
	}
	ciphertext, err := crypto.Encrypt(note.Content, password)
	if err != nil {
		return 0, err
	}

	if !s.serverEvents {
		if err := model.SetEncrypted(id, userID, string(hash), ciphertext); err != nil {
			return 0, err
		}
		return readVersion(id)
	}
	// server 模式：事务内 CAS（is_encrypted=0 + version 基点）→ version++ → UPDATE 事件同提交。
	tx, err := database.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := model.SetEncryptedTx(tx, id, userID, string(hash), ciphertext); err != nil {
		return 0, err
	}
	v, err := emitAfterReadTx(tx, userID, id, "UPDATE")
	if err != nil {
		return 0, err
	}
	return v, tx.Commit()
}

// DecryptNote 加密态 → 明文落回（口令验证通过后）。返回解密明文供 UI 直接编辑。
func (s *NoteService) DecryptNote(id, userID int64, password string) (string, error) {
	note, err := model.GetNote(id, userID)
	if err != nil {
		return "", err
	}
	if !note.IsEncrypted {
		return "", ErrNotEncrypted
	}
	passwordHash, ciphertext, err := model.GetEncryptedContent(id, userID)
	if err != nil {
		return "", err
	}
	if err := s.verifyPassword(passwordHash, password); err != nil {
		return "", err
	}
	plaintext, err := crypto.Decrypt(ciphertext, password)
	if err != nil {
		return "", ErrCorruptPayload
	}

	if !s.serverEvents {
		if err := model.ClearEncrypted(id, userID, plaintext, htmltext.ToText(plaintext)); err != nil {
			return "", err
		}
		return plaintext, nil
	}
	// server 模式：事务内 CAS（is_encrypted=1）→ version++ → UPDATE 事件同提交。
	tx, err := database.DB.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err := model.ClearEncryptedTx(tx, id, userID, plaintext, htmltext.ToText(plaintext)); err != nil {
		return "", err
	}
	if _, err := emitAfterReadTx(tx, userID, id, "UPDATE"); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return plaintext, nil
}

// VerifyEncrypt 校验口令并返回带明文的完整笔记（不落库！内存态，语义同 handler）。
func (s *NoteService) VerifyEncrypt(id, userID int64, password string) (*model.Note, error) {
	passwordHash, ciphertext, err := model.GetEncryptedContent(id, userID)
	if err != nil {
		return nil, err
	}
	if err := s.verifyPassword(passwordHash, password); err != nil {
		return nil, err
	}
	plaintext, err := crypto.Decrypt(ciphertext, password)
	if err != nil {
		return nil, ErrCorruptPayload
	}
	note, err := model.GetNote(id, userID)
	if err != nil {
		return nil, err
	}
	note.Content = plaintext
	note.IsEncrypted = true
	return note, nil
}

// UpdateEncrypted 加密笔记保存：验证旧口令 → 新密文+新口令哈希 → 单事务 CAS（v21.52 路径）。
// baseVersion = 客户端已知服务端版本（ADR-002 桌面 Outbox 的 CAS 基点）。
// server 模式补 UPDATE 事件（§60，版本由 CAS 自增推进）。
func (s *NoteService) UpdateEncrypted(id, userID int64, title, tags, plaintext, password string, baseVersion int64) (*model.Note, error) {
	passwordHash, _, err := model.GetEncryptedContent(id, userID)
	if err != nil {
		return nil, err
	}
	if err := s.verifyPassword(passwordHash, password); err != nil {
		return nil, err
	}
	if err := validateEncryptPassword(password); err != nil {
		return nil, err
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, ErrPasswordInvalid
	}
	ciphertext, err := crypto.Encrypt(plaintext, password)
	if err != nil {
		return nil, err
	}

	if !s.serverEvents {
		if err := model.UpdateEncryptedNote(id, userID, title, tags, string(newHash), ciphertext, plaintext, baseVersion); err != nil {
			return nil, err
		}
		return model.GetNote(id, userID)
	}
	// server 模式：同一事务内 CAS 更新 + UPDATE 事件。
	tx, err := database.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	newVersion, err := model.UpdateEncryptedNoteTx(tx, id, userID, title, tags, string(newHash), ciphertext, plaintext, baseVersion)
	if err != nil {
		return nil, err
	}
	if err := emitNoteChange(tx, userID, id, "UPDATE", newVersion); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return model.GetNote(id, userID)
}

// emitAfterReadTx 事件版本从事务内读回（Set/ClearEncryptedTx 的 CAS UPDATE 已内置 version++，
// 与 CRUD 的 bump 分离模式不同——CAS 谓词须与自增同语句才有原子性）。
func emitAfterReadTx(tx *sql.Tx, userID, noteID int64, operation string) (int64, error) {
	var v int64
	if err := tx.QueryRow(`SELECT version FROM notes WHERE id = ?`, noteID).Scan(&v); err != nil {
		return 0, fmt.Errorf("read note %d version: %w", noteID, err)
	}
	return v, emitNoteChange(tx, userID, noteID, operation, v)
}

// readVersion 读回实体当前版本（桌面非事务路径用）。
func readVersion(noteID int64) (int64, error) {
	var v int64
	if err := database.DB.QueryRow(`SELECT version FROM notes WHERE id = ?`, noteID).Scan(&v); err != nil {
		return 0, fmt.Errorf("read note %d version: %w", noteID, err)
	}
	return v, nil
}

func (s *NoteService) verifyPassword(hash, password string) error {
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return ErrWrongPassword
	}
	return nil
}

func validateEncryptPassword(password string) error {
	if len([]byte(password)) > 72 {
		return ErrPasswordTooLong
	}
	return nil
}
