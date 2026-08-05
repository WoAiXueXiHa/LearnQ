package imagestore

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// MaxBytes 为图片大小硬上限（5 MiB）：上传、存储、读取三处均以该值校验。
const MaxBytes = 5 << 20

// Store 把图片以文件形式保存在 Dir 目录下。对外暴露的文件名都是不含路径分隔符的
// 相对名（由 Save 生成），读取/删除时经 resolve 拼回完整路径。
type Store struct {
	Dir string
}

// New 创建指向 dir 目录的图片存储。
func New(dir string) *Store { return &Store{Dir: dir} }

// Save 将 body 原子写入 s.Dir：先写同目录临时文件再 rename，读者永远看不到半写状态。
// 返回不含目录的文件名（<32位hex>.<ext>），由调用方持久化；任一环节失败时清理临时文件，
// 保证磁盘上不残留孤儿数据。
// 入参约束：body 须非空且不超过 MaxBytes；extension 仅接受 ".jpg" 或 ".png"。
func (s *Store) Save(body []byte, extension string) (string, error) {
	if len(body) == 0 || len(body) > MaxBytes {
		return "", fmt.Errorf("image size must be between 1 byte and %d bytes", MaxBytes)
	}
	if extension != ".jpg" && extension != ".png" {
		return "", errors.New("unsupported image extension")
	}
	// 目录按需创建；0o700 使图片目录仅属主可读写，避免同机其他用户窥探图片。
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return "", err
	}
	// 文件名用 16 字节加密随机数而非用户原始文件名：不可预测、不会重名覆盖，
	// 且不含路径分隔符（与 resolve 的路径校验相互配合）。
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	name := hex.EncodeToString(random[:]) + extension
	// 临时文件与目标文件同目录：保证后续 rename 在同一文件系统内（跨设备 rename 会失败）。
	temp, err := os.CreateTemp(s.Dir, ".upload-*")
	if err != nil {
		return "", err
	}
	tempName := temp.Name()
	// cleanup 收敛所有失败路径的收尾：关闭句柄并删除临时文件，避免磁盘残留。
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempName)
	}
	// 显式收紧为 0o600：最终图片文件的权限不依赖 umask 或平台默认值。
	if err := temp.Chmod(0o600); err != nil {
		cleanup()
		return "", err
	}
	if _, err := temp.Write(body); err != nil {
		cleanup()
		return "", err
	}
	// Sync 把数据刷入磁盘：缺了它，进程崩溃后可能 rename 出一个内容不完整的"正式"文件。
	if err := temp.Sync(); err != nil {
		cleanup()
		return "", err
	}
	// 先 Close 再 rename：保证文件内容全部落盘后才在目录中"公开"这个名字。
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempName)
		return "", err
	}
	// rename 是同目录内的原子替换：从此刻起读者能看到的只有完整文件，没有中间态。
	if err := os.Rename(tempName, filepath.Join(s.Dir, name)); err != nil {
		_ = os.Remove(tempName)
		return "", err
	}
	return name, nil
}

// Open 打开名为 name 的图片文件。name 必须是不含路径分隔符的纯文件名（见 resolve），
// 返回的文件由调用方负责 Close。
func (s *Store) Open(name string) (*os.File, error) {
	path, err := s.resolve(name)
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}

// Read 读回整张图片。LimitReader 多读 1 字节用于探测超限：若读满 MaxBytes+1，
// 说明磁盘文件异常膨胀（正常写入路径受 Save 限制不会超限），直接报错而非把超限数据喂给下游。
func (s *Store) Read(name string) ([]byte, error) {
	file, err := s.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > MaxBytes {
		return nil, errors.New("stored image exceeds size limit")
	}
	return body, nil
}

// Delete 删除图片文件；文件本就不存在时视为成功（幂等），
// 供"上传后建库失败回滚"与"删除流程重试"两条路径复用。
func (s *Store) Delete(name string) error {
	path, err := s.resolve(name)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	// 目标已被删过（如重试场景）不算错误，保证删除可重复执行。
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// resolve 把存储名解析为完整路径。要求 name 非空且 filepath.Base(name) == name：
// 含路径分隔符的名字（如 "../x"、"/abs/path"、"a/../b"）一律拒绝，可用名只能是不含
// 分隔符的纯文件名（正常路径由 Save 生成）。数据库中的 StoragePath 视为不可信输入，
// 此校验阻断夹带路径元素的目录穿越写法。
func (s *Store) resolve(name string) (string, error) {
	if name == "" || filepath.Base(name) != name {
		return "", errors.New("invalid image storage path")
	}
	return filepath.Join(s.Dir, name), nil
}
