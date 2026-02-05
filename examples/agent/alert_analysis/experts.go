package main

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/components/model"
	// openai is from eino-ext repository
	// "github.com/cloudwego/eino-ext/components/model/openai"
)

// ExpertType 专家类型
type ExpertType string

const (
	FaultDiagnosisExpert    ExpertType = "fault_diagnosis"    // 故障诊断专家
	PerformanceAnalysisExpert ExpertType = "performance_analysis" // 性能分析专家
	BusinessImpactExpert    ExpertType = "business_impact"    // 业务影响专家
)

// ExpertAgent 专家 Agent
type ExpertAgent struct {
	expertType  ExpertType
	name        string
	description string
	instruction string
	chatModel   model.ChatModel
}

// NewExpertAgent 创建专家 Agent
func NewExpertAgent(ctx context.Context, expertType ExpertType, chatModel model.ChatModel) (*ExpertAgent, error) {
	// 验证 chatModel
	if chatModel == nil {
		return nil, fmt.Errorf("chatModel cannot be nil")
	}

	// 根据专家类型配置
	var name, description, instruction string

	switch expertType {
	case FaultDiagnosisExpert:
		name = "故障诊断专家"
		description = "专门负责分析系统故障的根本原因"
		instruction = `你是一位资深的故障诊断专家，具备深厚的技术背景和丰富的故障排查经验。

你的职责：
1. 分析告警数据，识别故障症状和模式
2. 追踪故障传播路径，找到根本原因
3. 评估故障的严重程度和影响范围
4. 提供具体的故障定位和排查建议

分析方法：
- 从系统架构角度分析依赖关系
- 关注时间序列，识别故障传播链
- 分析相关日志和指标
- 识别常见的故障模式（如级联故障、资源耗尽等）

输出格式：
1. 根因分析：列出可能的根因及其可能性
2. 关键发现：列出分析过程中的关键发现
3. 诊断置信度：给出你对该诊断的信心程度（0-1）
4. 建议措施：列出具体的排查和修复建议`

	case PerformanceAnalysisExpert:
		name = "性能分析专家"
		description = "专门负责分析系统性能指标和瓶颈"
		instruction = `你是一位资深的性能分析专家，精通系统性能优化和资源管理。

你的职责：
1. 分析性能指标（CPU、内存、网络、I/O、延迟等）
2. 识别性能瓶颈和资源争用问题
3. 评估当前性能基线和异常偏差
4. 提供性能优化建议

分析方法：
- 关注关键性能指标（KPI）的变化趋势
- 识别资源利用率的异常峰值
- 分析系统容量和扩展能力
- 评估性能问题的业务影响

输出格式：
1. 性能评估：当前性能状态评估
2. 瓶颈识别：列出主要的性能瓶颈
3. 趋势分析：分析性能指标的变化趋势
4. 优化建议：提供具体的性能优化方案
5. 诊断置信度：给出你对该分析的信心程度（0-1）`

	case BusinessImpactExpert:
		name = "业务影响专家"
		description = "专门负责评估故障对业务的影响"
		instruction = `你是一位资深的业务分析专家，精通IT系统与业务价值的关联分析。

你的职责：
1. 评估故障对业务流程的影响程度
2. 分析受影响的用户群体和业务场景
3. 估算潜在的业务损失
4. 提供业务连续性保障建议

分析方法：
- 关联系统功能与业务流程
- 评估用户影响范围和体验下降程度
- 分析交易量、收入等业务指标
- 考虑合规性和SLA要求

输出格式：
1. 影响评估：业务影响程度和范围
2. 用户影响：受影响的用户群体和场景
3. 损失估算：预估的业务损失
4. 紧急程度：根据业务影响给出处理优先级
5. 应对建议：提供业务层面的应对措施
6. 诊断置信度：给出你对该评估的信心程度（0-1）`

	default:
		return nil, fmt.Errorf("unknown expert type: %s", expertType)
	}

	return &ExpertAgent{
		expertType:  expertType,
		name:        name,
		description: description,
		instruction: instruction,
		chatModel:   chatModel,
	}, nil
}

// Analyze 执行分析
func (e *ExpertAgent) Analyze(ctx context.Context, context AnalysisContext) (*ExpertAnalysisResult, error) {
	// 构建分析提示
	_ = e.buildAnalysisPrompt(context) // 为将来使用 ChatModel 做准备

	// 调用模型进行分析
	// 这里简化了实际的调用过程
	fmt.Printf("[%s] 正在分析 %d 个告警 (迭代轮次: %d)\n",
		e.name, len(context.Alarms), context.Iteration)

	// TODO: 实际调用 ChatModel 进行分析
	// result, err := e.chatModel.Generate(ctx, prompt)

	// 模拟返回结果
	result := &ExpertAnalysisResult{
		ExpertName:  e.name,
		ExpertType:  string(e.expertType),
		Analysis:    fmt.Sprintf("%s 的分析结果: 发现 %d 个关键问题", e.name, len(context.Alarms)),
		Findings:    []string{
			fmt.Sprintf("发现 %s 相关的异常模式", e.name),
			"需要进一步验证",
		},
		Confidence: 0.75,
		Recommendations: []string{
			"建议立即排查",
			"建议加强监控",
		},
	}

	return result, nil
}

// buildAnalysisPrompt 构建分析提示
func (e *ExpertAgent) buildAnalysisPrompt(context AnalysisContext) string {
	prompt := fmt.Sprintf("%s\n\n", e.instruction)
	prompt += fmt.Sprintf("## 当前分析上下文\n")
	prompt += fmt.Sprintf("- 迭代轮次: %d/%d\n", context.Iteration, context.MaxRounds)
	prompt += fmt.Sprintf("- 告警数量: %d\n\n", len(context.Alarms))

	if len(context.PrevResults) > 0 {
		prompt += "## 之前轮次的分析结果\n"
		for i, result := range context.PrevResults {
			prompt += fmt.Sprintf("### 第 %d 轮\n%s\n\n", i, result)
		}
	}

	prompt += "## 当前告警列表\n"
	for i, alarm := range context.Alarms {
		prompt += fmt.Sprintf("### 告警 %d: %s\n", i+1, alarm.Title)
		prompt += fmt.Sprintf("- 级别: %s\n", alarm.Level)
		prompt += fmt.Sprintf("- 服务: %s\n", alarm.Service)
		prompt += fmt.Sprintf("- 描述: %s\n", alarm.Description)
		if len(alarm.Metrics) > 0 {
			prompt += "- 关键指标:\n"
			for k, v := range alarm.Metrics {
				prompt += fmt.Sprintf("  - %s: %.2f\n", k, v)
			}
		}
		prompt += "\n"
	}

	prompt += "\n请基于以上信息进行专业分析，并按照指定格式输出结果。"

	return prompt
}

// GetName 获取专家名称
func (e *ExpertAgent) GetName() string {
	return e.name
}

// GetType 获取专家类型
func (e *ExpertAgent) GetType() ExpertType {
	return e.expertType
}
