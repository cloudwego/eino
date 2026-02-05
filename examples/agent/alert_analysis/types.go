package main

import (
	"encoding/json"
	"time"
)

// Alarm 表示一个告警事件
type Alarm struct {
	ID          string                 `json:"id"`
	Level       string                 `json:"level"`       // critical, warning, info
	Title       string                 `json:"title"`
	Description string                 `json:"description"`
	Service     string                 `json:"service"`
	Metrics     map[string]float64     `json:"metrics"`
	Labels      map[string]string      `json:"labels"`
	Timestamp   time.Time              `json:"timestamp"`
	Metadata    map[string]any `json:"metadata"`
}

// AlarmPool 告警池，存储一段时间内的所有告警
type AlarmPool struct {
	alarms      []Alarm
	maxSize     int
	retention   time.Duration
}

// NewAlarmPool 创建新的告警池
func NewAlarmPool(maxSize int, retention time.Duration) *AlarmPool {
	return &AlarmPool{
		alarms:    make([]Alarm, 0, maxSize),
		maxSize:   maxSize,
		retention: retention,
	}
}

// Add 添加告警到池中
func (ap *AlarmPool) Add(alarm Alarm) {
	ap.alarms = append(ap.alarms, alarm)

	// 清理过期告警
	cutoff := time.Now().Add(-ap.retention)
	filtered := make([]Alarm, 0)
	for _, a := range ap.alarms {
		if a.Timestamp.After(cutoff) {
			filtered = append(filtered, a)
		}
	}
	ap.alarms = filtered

	// 保持最大大小限制
	if len(ap.alarms) > ap.maxSize {
		ap.alarms = ap.alarms[len(ap.alarms)-ap.maxSize:]
	}
}

// GetRecent 获取最近的告警
func (ap *AlarmPool) GetRecent() []Alarm {
	return ap.alarms
}

// GetByLevel 根据级别获取告警
func (ap *AlarmPool) GetByLevel(level string) []Alarm {
	result := make([]Alarm, 0)
	for _, alarm := range ap.alarms {
		if alarm.Level == level {
			result = append(result, alarm)
		}
	}
	return result
}

// AnalysisContext 分析上下文
type AnalysisContext struct {
	Alarms      []Alarm  `json:"alarms"`
	Iteration   int      `json:"iteration"`
	MaxRounds   int      `json:"maxRounds"`
	PrevResults []string `json:"prevResults,omitempty"` // 之前轮次的分析结果
}

// ExpertAnalysisResult 专家分析结果
type ExpertAnalysisResult struct {
	ExpertName  string `json:"expertName"`
	ExpertType  string `json:"expertType"`
	Analysis    string `json:"analysis"`
	Findings    []string `json:"findings"`
	Confidence  float64 `json:"confidence"`
	Recommendations []string `json:"recommendations"`
}

// ManagerDecision Manager 决策
type ManagerDecision struct {
	ContinueAnalysis bool     `json:"continueAnalysis"` // 是否需要继续分析
	Reasoning        string   `json:"reasoning"`         // 决策理由
	NextSteps        []string `json:"nextSteps"`         // 下一轮需要关注的重点
	Decision         string   `json:"decision"`          // 决策内容
}

// FinalReport 最终报告
type FinalReport struct {
	Summary              string                   `json:"summary"`
	RootCause            string                   `json:"rootCause"`
	BusinessImpact       string                   `json:"businessImpact"`
	PerformanceAnalysis  string                   `json:"performanceAnalysis"`
	Recommendations      []string                 `json:"recommendations"`
	Priority             string                   `json:"priority"`
	EstimatedResolution  time.Duration            `json:"estimatedResolution"`
	ExpertResults        map[string]ExpertAnalysisResult `json:"expertResults"`
	IterationsCompleted  int                      `json:"iterationsCompleted"`
}

// AnalysisTask 分析任务
type AnalysisTask struct {
	TaskID      string          `json:"taskId"`
	Context     AnalysisContext `json:"context"`
	Status      string          `json:"status"` // pending, in_progress, completed, failed
	CreatedAt   time.Time       `json:"createdAt"`
	CompletedAt *time.Time      `json:"completedAt,omitempty"`
	Result      *FinalReport    `json:"result,omitempty"`
	Error       string          `json:"error,omitempty"`
}

// MarshalJSON 自定义 JSON 序列化
func (a Alarm) MarshalJSON() ([]byte, error) {
	type Alias Alarm
	return json.Marshal(&struct {
		Timestamp string `json:"timestamp"`
		*Alias
	}{
		Timestamp: a.Timestamp.Format(time.RFC3339),
		Alias:     (*Alias)(&a),
	})
}

// UnmarshalJSON 自定义 JSON 反序列化
func (a *Alarm) UnmarshalJSON(data []byte) error {
	type Alias Alarm
	aux := &struct {
		Timestamp string `json:"timestamp"`
		*Alias
	}{
		Alias: (*Alias)(a),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if aux.Timestamp != "" {
		t, err := time.Parse(time.RFC3339, aux.Timestamp)
		if err != nil {
			return err
		}
		a.Timestamp = t
	}
	return nil
}
