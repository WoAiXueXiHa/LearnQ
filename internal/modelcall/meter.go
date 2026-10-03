// Package modelcall reserves a configured per-request budget before calling
// providers. Reservations are conservative accounting, not billing evidence.
package modelcall

import (
	"context"
	"errors"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/WoAiXueXiHa/LearnQ/internal/model"
	"gorm.io/gorm"
)

type Meter struct {
	DB            *gorm.DB
	Mode          string
	DailyMicroCNY int64
	CallMicroCNY  int64
}

func (m *Meter) begin(ctx context.Context, kind, name string, inputBound, outputBound int) (uint64, time.Time, error) {
	started := time.Now().UTC()
	reserve := m.CallMicroCNY
	if m.Mode != "real" {
		reserve = 0
	} else if reserve <= 0 || m.DailyMicroCNY <= 0 {
		return 0, started, model.Permanent(errors.New("MODEL_BUDGET_EXHAUSTED: real calls require a positive budget and reservation"))
	}
	var id uint64
	err := m.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var price domain.ModelPrice
		if m.Mode == "real" {
			if err := tx.Where("model=?", name).Order("id DESC").First(&price).Error; err != nil {
				return model.Permanent(errors.New("MODEL_PRICE_MISSING: configure model price before real calls"))
			}
			if inputBound > price.MaxInputTokens || outputBound > price.MaxOutputTokens {
				return model.Permanent(errors.New("MODEL_INPUT_LIMIT: request estimate exceeds configured token limits"))
			}
			estimate := (int64(inputBound)*price.InputMicroCNYPerMillion + int64(outputBound)*price.OutputMicroCNYPerMillion + 999999) / 1000000
			if estimate > reserve {
				return model.Permanent(errors.New("MODEL_CALL_BUDGET_EXHAUSTED: estimated request exceeds per-call reservation"))
			}
		}
		day := started.Format("2006-01-02")
		if err := tx.Exec("INSERT IGNORE INTO model_daily_budgets(day,reserved_microcny) VALUES (?,0)", day).Error; err != nil {
			return err
		}
		if reserve > 0 {
			result := tx.Exec("UPDATE model_daily_budgets SET reserved_microcny=reserved_microcny+? WHERE day=? AND reserved_microcny<=?", reserve, day, m.DailyMicroCNY-reserve)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return model.Permanent(errors.New("MODEL_BUDGET_EXHAUSTED: daily reservation limit reached"))
			}
		}
		result := tx.Exec("INSERT INTO model_calls(kind,mode,model,status,reserved_microcny,input_tokens,output_tokens,duration_ms,error_code,started_at) VALUES (?,?,?,'started',?,0,0,0,'',?)", kind, m.Mode, name, reserve, started)
		if result.Error != nil {
			return result.Error
		}
		if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error; err != nil {
			return err
		}
		if m.Mode == "real" {
			return tx.Exec("UPDATE model_calls SET model_price_id=? WHERE id=?", price.ID, id).Error
		}
		return tx.Exec("UPDATE model_calls SET estimated_microcny=0,estimate_basis='fake' WHERE id=?", id).Error
	})
	return id, started, err
}

func (m *Meter) finish(id uint64, started time.Time, name string, input, output int, cause error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	status, code := "succeeded", ""
	if cause != nil {
		status = "uncertain"
		code = "PROVIDER_CALL_FAILED"
	}
	return m.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("UPDATE model_calls SET status=?,model=?,input_tokens=?,output_tokens=?,usage_known=?,duration_ms=?,error_code=?,finished_at=? WHERE id=? AND status='started'", status, name, input, output, input > 0 || output > 0, time.Since(started).Milliseconds(), code, time.Now().UTC(), id).Error; err != nil {
			return err
		}
		if m.Mode == "real" && cause == nil && (input > 0 || output > 0) {
			return tx.Exec("UPDATE model_calls c JOIN model_prices p ON p.id=c.model_price_id SET c.estimated_microcny=CEIL((?*p.input_microcny_per_million+?*p.output_microcny_per_million)/1000000),c.estimate_basis='provider_tokens_configured_price' WHERE c.id=?", input, output, id).Error
		}
		return nil
	})
}

type Chat struct {
	Meter *Meter
	Inner model.ChatModel
	Name  string
}

func (c Chat) Generate(ctx context.Context, req model.ChatRequest) (model.ChatResponse, error) {
	if req.MaxOutputTokens <= 0 {
		req.MaxOutputTokens = 4096
	}
	id, started, err := c.Meter.begin(ctx, "text:"+req.Skill, c.Name, len(req.Input)+len(req.Prompt)+len(req.ResponseSchema)+512, req.MaxOutputTokens)
	if err != nil {
		return model.ChatResponse{}, err
	}
	response, err := c.Inner.Generate(ctx, req)
	name := response.Model
	if name == "" {
		name = c.Name
	}
	if recordErr := c.Meter.finish(id, started, name, response.InputTokens, response.OutputTokens, err); err == nil && recordErr != nil {
		err = recordErr
	}
	return response, err
}

type Embedding struct {
	Meter *Meter
	Inner model.EmbeddingModel
	Name  string
}

func (e Embedding) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	inputBound := 0
	for _, text := range texts {
		inputBound += len(text)
	}
	id, started, err := e.Meter.begin(ctx, "embedding", e.Name, inputBound, 0)
	if err != nil {
		return nil, err
	}
	var response [][]float32
	tokens := 0
	if provider, ok := e.Inner.(interface {
		EmbedWithUsage(context.Context, []string) ([][]float32, int, error)
	}); ok {
		response, tokens, err = provider.EmbedWithUsage(ctx, texts)
	} else {
		response, err = e.Inner.Embed(ctx, texts)
	}
	// Zero with usage_known=false denotes missing provider metadata.
	if recordErr := e.Meter.finish(id, started, e.Name, tokens, 0, err); err == nil && recordErr != nil {
		err = recordErr
	}
	return response, err
}

type Vision struct {
	Meter *Meter
	Inner model.VisionModel
	Name  string
}

func (v Vision) Describe(ctx context.Context, req model.VisionRequest) (model.VisionResponse, error) {
	if req.MaxOutputTokens <= 0 {
		req.MaxOutputTokens = 4096
	}
	id, started, err := v.Meter.begin(ctx, "vision", v.Name, len(req.Image)*4/3+len(req.Prompt)+512, req.MaxOutputTokens)
	if err != nil {
		return model.VisionResponse{}, err
	}
	response, err := v.Inner.Describe(ctx, req)
	name := response.Model
	if name == "" {
		name = v.Name
	}
	if recordErr := v.Meter.finish(id, started, name, response.InputTokens, response.OutputTokens, err); err == nil && recordErr != nil {
		err = recordErr
	}
	return response, err
}
