package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"

	// 两个 blank import 注册 image/jpeg 与 image/png 解码器，image.DecodeConfig 才有能力识别这两种格式。
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/WoAiXueXiHa/LearnQ/internal/imagestore"
	"github.com/WoAiXueXiHa/LearnQ/internal/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 上传校验的三个硬上限：
// maxImageMultipartBytes——multipart 请求整体上限，除图片 5 MiB 外为表单其他字段（prompt、study_record_id）留 1 MiB 余量；
// maxImagePromptRunes——提示词按 rune 计数，按字节计数会把多字节中文截成半个字符；
// maxImagePixels——像素总数上限，拒绝超大图，避免后续 Vision 推理被超大尺寸拖垮。
const (
	maxImageMultipartBytes = imagestore.MaxBytes + (1 << 20)
	maxImagePromptRunes    = 1000
	maxImagePixels         = 20_000_000
)

// uploadImage 处理图片上传。校验链依次为：请求总大小、文件大小、文件名、MIME 与扩展名一致性、
// 像素数、prompt 长度、关联 study_record_id 存在性；全部通过后才落盘并写库。
// 返回 202 而非 200：图片描述由 image_describe 任务异步产出，此处只确认已受理。
func (s *Server) uploadImage(c *gin.Context) {
	if s.imageStore == nil {
		fail(c, 503, "DEPENDENCY_UNAVAILABLE", "image storage is not configured", nil)
		return
	}
	// MaxBytesReader 在读取层直接拦截超限请求，出错时返回 *http.MaxBytesError，
	// 比"先读完再量尺寸"更早失败、更省内存。
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxImageMultipartBytes)
	// 1 MiB 是内存缓冲阈值：超过部分的表单数据由框架落临时文件，避免大请求撑爆内存。
	if err := c.Request.ParseMultipartForm(1 << 20); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			fail(c, 413, "VALIDATION_FAILED", "multipart request exceeds 6 MiB", nil)
			return
		}
		// 其余解析错误（非 multipart、缺字段等）统一按"缺文件"报 422。
		fail(c, 422, "VALIDATION_FAILED", "multipart image file is required", nil)
		return
	}
	// 清理 ParseMultipartForm 写入磁盘的临时文件（仅表单超内存阈值时存在）。
	if c.Request.MultipartForm != nil {
		defer c.Request.MultipartForm.RemoveAll()
	}
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		fail(c, 422, "VALIDATION_FAILED", "multipart image file is required", nil)
		return
	}
	defer file.Close()
	// 多读 1 字节用于探测超限：若实际读满 MaxBytes+1，则说明文件超过 5 MiB，随后被 413 拦下。
	body, err := io.ReadAll(io.LimitReader(file, imagestore.MaxBytes+1))
	if err != nil {
		fail(c, 422, "VALIDATION_FAILED", "image could not be read", nil)
		return
	}
	if len(body) == 0 || len(body) > imagestore.MaxBytes {
		fail(c, 413, "VALIDATION_FAILED", "image must not exceed 5 MiB", nil)
		return
	}
	// 文件名去除首尾空白后须非空且不超过 255 个字符（rune 计），避免空名与超长名入库。
	header.Filename = strings.TrimSpace(header.Filename)
	if header.Filename == "" || utf8.RuneCountInString(header.Filename) > 255 {
		fail(c, 422, "VALIDATION_FAILED", "filename is required and must not exceed 255 characters", nil)
		return
	}
	// MIME 由内容嗅探得出（http.DetectContentType 只看文件头），再与扩展名交叉校验：
	// 存储与返回的 media_type 以内容为准，防止改扩展名的文件蒙混过关。
	mediaType := http.DetectContentType(body)
	extension := strings.ToLower(filepath.Ext(header.Filename))
	switch mediaType {
	case "image/jpeg":
		// 扩展名必须与检测到的内容类型一致；落盘统一规范为 .jpg。
		if extension != ".jpg" && extension != ".jpeg" {
			fail(c, 422, "VALIDATION_FAILED", "JPEG content requires a .jpg or .jpeg filename", nil)
			return
		}
		extension = ".jpg"
	case "image/png":
		if extension != ".png" {
			fail(c, 422, "VALIDATION_FAILED", "PNG content requires a .png filename", nil)
			return
		}
	default:
		fail(c, 422, "VALIDATION_FAILED", "only PNG and JPEG images are supported", nil)
		return
	}
	// DecodeConfig 只解析图像头部而非完整解码：既能验证格式合法性，又能以极小成本
	// 拿到宽高做像素数校验。
	config, _, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil || config.Width <= 0 || config.Height <= 0 ||
		// 先转 int64 再相乘：32 位平台上 int 乘法可能溢出，导致超大尺寸被误判为合法。
		int64(config.Width)*int64(config.Height) > maxImagePixels {
		fail(c, 422, "VALIDATION_FAILED", "image dimensions are invalid or exceed 20 megapixels", nil)
		return
	}
	prompt := strings.TrimSpace(c.Request.FormValue("prompt"))
	if utf8.RuneCountInString(prompt) > maxImagePromptRunes {
		fail(c, 422, "VALIDATION_FAILED", "prompt must not exceed 1000 characters", nil)
		return
	}
	// study_record_id 为可选字段：提供时须是正整数且记录真实存在，防止图片挂到不存在的学习记录上。
	var studyRecordID *uint64
	if raw := strings.TrimSpace(c.Request.FormValue("study_record_id")); raw != "" {
		value, parseErr := strconv.ParseUint(raw, 10, 64)
		// ParseUint 对 "0" 不会报错，这里显式拒绝 0，避免与"未提供"（nil）语义混淆。
		if parseErr != nil || value == 0 {
			fail(c, 422, "VALIDATION_FAILED", "study_record_id must be a positive integer", nil)
			return
		}
		var count int64
		if err := s.store.DB.Model(&domain.StudyRecord{}).Where("id=?", value).Count(&count).Error; err != nil {
			fail(c, 500, "INTERNAL_ERROR", "could not validate study record", nil)
			return
		}
		if count != 1 {
			fail(c, 404, "NOT_FOUND", "study record was not found", nil)
			return
		}
		studyRecordID = &value
	}
	// 先落盘再建库，顺序不可反：若建库失败（事务内创建 image+task+outbox），
	// 回滚删除已落盘的文件，避免磁盘残留孤儿图片。
	storagePath, err := s.imageStore.Save(body, extension)
	if err != nil {
		fail(c, 500, "INTERNAL_ERROR", "image could not be stored", nil)
		return
	}
	// 内容指纹（SHA-256）随行入库，与普通文档上传路径的做法一致。
	sum := sha256.Sum256(body)
	imageRow, _, err := s.store.CreateImage(c.Request.Context(), domain.Image{
		StudyRecordID: studyRecordID, OriginalFilename: header.Filename,
		MediaType: mediaType, SizeBytes: int64(len(body)), Width: config.Width, Height: config.Height,
		ContentHash: hex.EncodeToString(sum[:]), StoragePath: storagePath, Prompt: prompt,
	})
	if err != nil {
		// 清理本步落盘的文件；Delete 对不存在的文件幂等，重复删除安全。
		_ = s.imageStore.Delete(storagePath)
		fail(c, 500, "INTERNAL_ERROR", "image metadata could not be saved", nil)
		return
	}
	// 202：任务已受理（image_describe 异步执行），描述结果稍后通过 GET /images/:id 查询。
	ok(c, http.StatusAccepted, imageView(imageRow))
}

// listImages 按 id 倒序返回最近 100 张图片的元数据（不含图片字节，原始内容走 imageContent）。
func (s *Server) listImages(c *gin.Context) {
	var images []domain.Image
	if err := s.store.DB.Order("id DESC").Limit(100).Find(&images).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not list images", nil)
		return
	}
	result := make([]gin.H, 0, len(images))
	for _, imageRow := range images {
		result = append(result, imageView(imageRow))
	}
	ok(c, 200, result)
}

// getImage 返回单张图片的元数据与描述结果；图片原始字节由 imageContent 单独提供。
func (s *Server) getImage(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	var imageRow domain.Image
	if lookupFailed(c, s.store.DB.First(&imageRow, id).Error, "could not load image") {
		return
	}
	ok(c, 200, imageView(imageRow))
}

// imageContent 以原始字节流返回图片文件（带 Content-Type），供页面 <img> 直接渲染；
// 不经过 JSON 序列化，避免大图被 base64/转义放大体积。
func (s *Server) imageContent(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	if s.imageStore == nil {
		fail(c, 503, "DEPENDENCY_UNAVAILABLE", "image storage is not configured", nil)
		return
	}
	var imageRow domain.Image
	if lookupFailed(c, s.store.DB.First(&imageRow, id).Error, "could not load image") {
		return
	}
	// deleting 是删除流程的中间态：文件可能已删或未删，此时对外一律视为不存在。
	if imageRow.Status == "deleting" {
		notFound(c)
		return
	}
	file, err := s.imageStore.Open(imageRow.StoragePath)
	if err != nil {
		// 磁盘文件丢失按 404 处理：元数据仍在但内容已不可得，对外语义与不存在一致。
		if errors.Is(err, os.ErrNotExist) {
			notFound(c)
			return
		}
		fail(c, 500, "INTERNAL_ERROR", "stored image could not be opened", nil)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		fail(c, 500, "INTERNAL_ERROR", "stored image could not be inspected", nil)
		return
	}
	// nosniff 禁止浏览器对响应做 MIME 嗅探：即使文件内容被改写成 HTML，也不会被当页面执行（防 XSS）。
	c.Header("X-Content-Type-Options", "nosniff")
	// inline 让浏览器直接展示图片而非强制下载。
	c.Header("Content-Disposition", "inline")
	c.DataFromReader(200, info.Size(), imageRow.MediaType, file, nil)
}

// deleteImage 执行两阶段删除：先经 PrepareImageDelete 把图片置为 deleting（同时终止关联任务、
// 将衍生文档一并置 deleting），再清 Qdrant 向量与磁盘文件，最后 FinalizeImageDelete 删除数据库行。
// 中间任一步失败时记录停留在 deleting 状态，客户端重试即可续删，不会产生半删残留。
func (s *Server) deleteImage(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	if s.imageStore == nil {
		fail(c, 503, "DEPENDENCY_UNAVAILABLE", "image storage is not configured", nil)
		return
	}
	imageRow, err := s.store.PrepareImageDelete(c.Request.Context(), id)
	// 记录不存在 → 404；状态竞争等其余失败 → 409，两种语义分开处理。
	if errors.Is(err, gorm.ErrRecordNotFound) {
		notFound(c)
		return
	}
	if err != nil {
		fail(c, 409, "IMAGE_STATE_CHANGED", "image could not enter deleting state", err.Error())
		return
	}
	// 已入知识库的图片需先移除其 Qdrant 向量，否则知识库会引用已删除的图片。
	if imageRow.DerivedDocumentID != nil {
		if s.vectors == nil {
			fail(c, 503, "DEPENDENCY_UNAVAILABLE", "vector storage is not configured; image remains deleting and may be retried", nil)
			return
		}
		// 向量删除失败不回滚 deleting 状态：图片保持"删除中"，重试时向量删除幂等。
		if err := s.vectors.DeleteDocument(c, *imageRow.DerivedDocumentID); err != nil {
			fail(c, 503, "DEPENDENCY_UNAVAILABLE",
				"could not remove derived Qdrant points; image remains deleting and may be retried", nil)
			return
		}
	}
	// 文件删除失败同样保持 deleting；Delete 对已不存在的文件幂等，重试不会报错。
	if err := s.imageStore.Delete(imageRow.StoragePath); err != nil {
		fail(c, 500, "INTERNAL_ERROR", "stored image could not be deleted; retry is safe", nil)
		return
	}
	// Finalize 是最后一步：只有前面的向量与文件都删干净，才真正删除数据库行。
	if err := s.store.FinalizeImageDelete(c.Request.Context(), id); err != nil {
		fail(c, 500, "INTERNAL_ERROR", "image deletion could not be finalized; retry is safe", nil)
		return
	}
	ok(c, 200, gin.H{"deleted": id})
}

// retryImage 手动重试描述失败的图片：仅 failed 状态可重试（状态约束在 store.RetryImage 内，
// 行锁 + 状态判断保证并发下只有一个请求能触发重试）。重试后任务 generation+1、图片回到 uploaded，
// 新描述任务经 outbox 异步入队，故返回 202。
func (s *Server) retryImage(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	imageRow, err := s.store.RetryImage(c.Request.Context(), id)
	// 不存在 → 404；状态不允许重试（如仍在处理中）→ 409，两者语义分开。
	if errors.Is(err, gorm.ErrRecordNotFound) {
		notFound(c)
		return
	}
	if err != nil {
		fail(c, 409, "IMAGE_NOT_RETRYABLE", err.Error(), nil)
		return
	}
	ok(c, http.StatusAccepted, imageView(imageRow))
}

// addImageToKnowledgeBase 把 ready 的图片描述转为 Markdown 文档并入知识库（索引任务异步执行）。
// 幂等：同一图片重复调用只会返回已有的衍生文档与其索引任务，不会重复创建（见 store.CreateDocumentFromImage）。
func (s *Server) addImageToKnowledgeBase(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	var imageRow domain.Image
	if lookupFailed(c, s.store.DB.First(&imageRow, id).Error, "could not load image") {
		return
	}
	// 双重前置条件：图片必须已完成描述（ready），且 description_json 能成功解析——
	// 描述数据损坏的图片不允许入知识库，避免把残缺内容索引给 RAG。
	var description model.ImageDescription
	if imageRow.Status != "ready" || json.Unmarshal([]byte(imageRow.DescriptionJSON), &description) != nil {
		fail(c, 409, "IMAGE_NOT_READY", "only a ready image may be added to the knowledge base", nil)
		return
	}
	content := imageDescriptionMarkdown(imageRow, description)
	// 衍生文档内容同样带 SHA-256 指纹（写入 documents.ContentHash），与普通文档上传路径一致。
	sum := sha256.Sum256([]byte(content))
	updated, document, task, err := s.store.CreateDocumentFromImage(
		c.Request.Context(), id, fmt.Sprintf("image-%d.md", id), content, hex.EncodeToString(sum[:]),
	)
	if err != nil {
		// 状态检查与 store 行锁复检之间存在窗口：图片被并发删除或重试（状态不再是 ready）时，
		// store 返回冲突错误 → 409；单纯并发 index 同一图片会被幂等分支吸收，两边都返回 202。
		fail(c, 409, "IMAGE_KNOWLEDGE_CONFLICT", err.Error(), nil)
		return
	}
	ok(c, http.StatusAccepted, gin.H{
		"image": imageView(updated), "document_id": document.ID, "indexing_task_id": task.ID,
	})
}

// imageView 把图片数据库行转换为 API 响应结构。
// 描述 JSON 缺失或损坏时按空描述兜底：KeyPoints/Uncertainties 序列化后是 [] 而非 null，
// 前端无需防御性地处理 null。
func imageView(imageRow domain.Image) gin.H {
	description := model.ImageDescription{KeyPoints: []string{}, Uncertainties: []string{}}
	if imageRow.DescriptionJSON != "" {
		_ = json.Unmarshal([]byte(imageRow.DescriptionJSON), &description)
		// 解析成功但字段为 null 时补成空数组，维持响应形状稳定。
		if description.KeyPoints == nil {
			description.KeyPoints = []string{}
		}
		if description.Uncertainties == nil {
			description.Uncertainties = []string{}
		}
	}
	return gin.H{
		"id": imageRow.ID, "original_filename": imageRow.OriginalFilename,
		"media_type": imageRow.MediaType, "size_bytes": imageRow.SizeBytes,
		"width": imageRow.Width, "height": imageRow.Height, "status": imageRow.Status,
		"task_id": imageRow.DescriptionTaskID, "description": description,
		"derived_document_id": imageRow.DerivedDocumentID, "last_error": imageRow.LastError,
		"created_at": imageRow.CreatedAt, "updated_at": imageRow.UpdatedAt,
	}
}

// imageDescriptionMarkdown 把 Vision 模型产出的描述结构渲染成 Markdown 文档，
// 该文本即为入知识库的衍生文档正文，之后会被切块并向量化。
func imageDescriptionMarkdown(imageRow domain.Image, description model.ImageDescription) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "# %s\n\n", description.Title)
	fmt.Fprintf(&builder, "- 原图片：%s\n- 摘要：%s\n\n", imageRow.OriginalFilename, description.Summary)
	if description.ExtractedText != "" {
		fmt.Fprintf(&builder, "## 图片文字\n\n%s\n\n", description.ExtractedText)
	}
	builder.WriteString("## 关键知识点\n\n")
	for _, point := range description.KeyPoints {
		fmt.Fprintf(&builder, "- %s\n", point)
	}
	fmt.Fprintf(&builder, "\n## 学习解释\n\n%s\n", description.LearningExplanation)
	if len(description.Uncertainties) > 0 {
		builder.WriteString("\n## 不确定内容\n\n")
		for _, uncertainty := range description.Uncertainties {
			fmt.Fprintf(&builder, "- %s\n", uncertainty)
		}
	}
	return builder.String()
}
