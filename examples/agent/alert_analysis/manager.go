package main

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudwego/eino/components/model"
	// openai is from eino-ext repository
	// "github.com/cloudwego/eino-ext/components/model/openai"
)

// ManagerAgent 管理 Agent，负责场景识别、计划生成、结果汇总和决策
type ManagerAgent struct {
	name        string
	chatModel   model.ChatModel
	experts     []*ExpertAgent
	maxRounds   int
}

// NewManagerAgent 创建 Manager Agent
func NewManagerAgent(ctx context.Context, chatModel model.ChatModel, experts []*ExpertAgent, maxRounds int) (*ManagerAgent, error) {
	// 验证 chatModel
	if chatModel == nil {
		return nil, fmt.Errorf("chatModel cannot be nil")
	}

	manager := &ManagerAgent{
		name:      "Manager",
		chatModel: chatModel,
		experts:   experts,
		maxRounds: maxRounds,
	}

	return manager, nil
}

// ScenarioRecognition 场景识别与计划生成
func (m *ManagerAgent) ScenarioRecognition(ctx context.Context, alarms []Alarm) (*AnalysisPlan, error) {
	fmt.Printf("[Manager] 正在进行场景识别和计划生成，告警数量: %d\n", len(alarms))

	// 构建场景识别提示
	_ = m.buildScenarioPrompt(alarms) // 为将来使用 ChatModel 做准备

	// TODO: 实际调用 ChatModel 进行场景识别
	// result, err := m.chatModel.Generate(ctx, prompt)

	// 分析告警特征
	scenario := m.analyzeScenario(alarms)

	plan := &AnalysisPlan{
		Scenario:      scenario.Type,
		Priority:      scenario.Priority,
		EstimatedRounds: scenario.EstimatedRounds,
		FocusAreas:    scenario.FocusAreas,
		Strategy:      scenario.Strategy,
	}

	fmt.Printf("[Manager] 场景识别结果: %s, 优先级: %s, 预计轮次: %d\n",
		plan.Scenario, plan.Priority, plan.EstimatedRounds)

	return plan, nil
}

// AnalyzeIteration 执行一轮迭代分析
func (m *ManagerAgent) AnalyzeIteration(ctx context.Context, context AnalysisContext) (*IterationResult, error) {
	fmt.Printf("[Manager] 开始第 %d 轮迭代分析\n", context.Iteration)

	// 并行调用所有专家
	expertResults := make([]*ExpertAnalysisResult, len(m.experts))
	resultCh := make(chan *ExpertAnalysisResult, len(m.experts))
	errorCh := make(chan error, len(m.experts))

	for i, expert := range m.experts {
		go func(idx int, exp *ExpertAgent) {
			result, err := exp.Analyze(ctx, context)
			if err != nil {
				errorCh <- err
				return
			}
			resultCh <- result
		}(i, expert)
	}

	// 收集结果
	for i := 0; i < len(m.experts); i++ {
		select {
		case result := <-resultCh:
			// 找到对应的专家位置
			for j, expert := range m.experts {
				if expert.GetName() == result.ExpertName {
					expertResults[j] = result
					break
				}
			}
		case err := <-errorCh:
			fmt.Printf("[ERROR] Expert analysis failed: %v\n", err)
		}
	}

	// Manager 汇总决策
	decision := m.makeDecision(ctx, context, expertResults)

	iterationResult := &IterationResult{
		Iteration:     context.Iteration,
		ExpertResults: expertResults,
		Decision:      decision,
		Timestamp:     time.Now().Unix(),
	}

	return iterationResult, nil
}

// makeDecision 做出决策
func (m *ManagerAgent) makeDecision(ctx context.Context, context AnalysisContext, results []*ExpertAnalysisResult) *ManagerDecision {
	fmt.Printf("[Manager] 正在进行决策汇总\n")

	// 构建决策提示
	_ = m.buildDecisionPrompt(context, results) // 为将来使用 ChatModel 做准备

	// TODO: 实际调用 ChatModel 进行决策
	// result, err := m.chatModel.Generate(ctx, prompt)

	// 简化的决策逻辑
	continueAnalysis := context.Iteration < m.maxRounds
	reasoning := "基于专家分析结果，需要进一步验证"

	// 检查是否有高置信度的结论
	hasHighConfidence := false
	for _, result := range results {
		if result.Confidence > 0.8 {
			hasHighConfidence = true
			break
		}
	}

	if hasHighConfidence && context.Iteration >= 2 {
		continueAnalysis = false
		reasoning = "已获得高置信度分析结果，可以结束迭代"
	}

	nextSteps := []string{
		"继续深入分析根因",
		"验证专家们的发现",
	}

	if !continueAnalysis {
		nextSteps = []string{"准备生成最终报告"}
	}

	decision := &ManagerDecision{
		ContinueAnalysis: continueAnalysis,
		Reasoning:        reasoning,
		NextSteps:        nextSteps,
		Decision:         fmt.Sprintf("第 %d 轮分析完成，%s", context.Iteration,
			map[bool]string{true: "继续迭代", false: "结束分析"}[continueAnalysis]),
	}

	fmt.Printf("[Manager] 决策: %s, 理由: %s\n", decision.Decision, decision.Reasoning)

	return decision
}

// GenerateReport 生成最终报告
func (m *ManagerAgent) GenerateReport(ctx context.Context, context *AnalysisContext, iterations []*IterationResult) (*FinalReport, error) {
	fmt.Printf("[Manager] 正在生成最终报告，已完成 %d 轮迭代\n", len(iterations))

	// 收集所有专家结果
	expertResults := make(map[string]ExpertAnalysisResult)
	for _, iteration := range iterations {
		for _, result := range iteration.ExpertResults {
			if result != nil {
				expertResults[result.ExpertName] = *result
			}
		}
	}

	// 构建报告生成提示
	_ = m.buildReportPrompt(context, iterations) // 为将来使用 ChatModel 做准备

	// TODO: 实际调用 ChatModel 生成报告
	// result, err := m.chatModel.Generate(ctx, prompt)

	// 简化的报告生成
	report := &FinalReport{
		Summary:             "检测到支付服务存在严重性能问题，可能导致业务损失",
		RootCause:           "数据库连接池配置不当导致连接耗尽，进而影响API响应时间",
		BusinessImpact:      "影响约1500名用户，支付成功率下降约5%，预计造成收入损失",
		PerformanceAnalysis: "P99延迟从200ms上升至2340ms，数据库连接使用率达到85%",
		Recommendations: []string{
			"立即增加数据库连接池大小",
			"优化慢查询，添加必要索引",
			"实施连接池监控和告警",
			"考虑实施读写分离架构",
		},
		Priority:            "P0 - 紧急",
		EstimatedResolution: 2 * 60 * 60 * 1000000000, // 2小时 (纳秒)
		ExpertResults:       expertResults,
		IterationsCompleted: len(iterations),
	}

	fmt.Printf("[Manager] 最终报告生成完成\n")
	fmt.Printf("  - 优先级: %s\n", report.Priority)
	fmt.Printf("  - 根因: %s\n", report.RootCause)
	fmt.Printf("  - 建议数量: %d\n", len(report.Recommendations))

	return report, nil
}

// buildScenarioPrompt 构建场景识别提示
func (m *ManagerAgent) buildScenarioPrompt(alarms []Alarm) string {
	prompt := `你是一个智能告警分析系统的 Manager。你的任务是分析一组告警，识别故障场景并制定分析计划。

请分析以下告警：
`

	for i, alarm := range alarms {
		prompt += fmt.Sprintf("\n### 告警 %d\n", i+1)
		prompt += fmt.Sprintf("- 标题: %s\n", alarm.Title)
		prompt += fmt.Sprintf("- 级别: %s\n", alarm.Level)
		prompt += fmt.Sprintf("- 服务: %s\n", alarm.Service)
		prompt += fmt.Sprintf("- 描述: %s\n", alarm.Description)
	}

	prompt += `
请输出：
1. 场景类型 (如: 性能问题、可用性问题、数据问题等)
2. 优先级 (P0/P1/P2)
3. 预计分析轮次 (1-5)
4. 重点分析领域
5. 分析策略
`

	return prompt
}

// buildDecisionPrompt 构建决策提示
func (m *ManagerAgent) buildDecisionPrompt(context AnalysisContext, results []*ExpertAnalysisResult) string {
	prompt := fmt.Sprintf("你正在管理第 %d/%d 轮分析。\n\n", context.Iteration, context.MaxRounds)
	prompt += "以下是各位专家的分析结果：\n\n"

	for _, result := range results {
		if result != nil {
			prompt += fmt.Sprintf("## %s (%s)\n", result.ExpertName, result.ExpertType)
			prompt += fmt.Sprintf("分析: %s\n", result.Analysis)
			prompt += fmt.Sprintf("置信度: %.2f\n\n", result.Confidence)
		}
	}

	prompt += `请基于以上分析结果做出决策：
1. 是否需要继续分析？
2. 如果继续，下一轮应该重点关注什么？
3. 如果不继续，是否可以生成最终报告？
`

	return prompt
}

// buildReportPrompt 构建报告生成提示
func (m *ManagerAgent) buildReportPrompt(context *AnalysisContext, iterations []*IterationResult) string {
	prompt := "请基于以下所有分析轮次的结果，生成一份完整的告警分析报告：\n\n"

	for i, iteration := range iterations {
		prompt += fmt.Sprintf("## 第 %d 轮分析\n", i+1)
		for _, result := range iteration.ExpertResults {
			if result != nil {
				prompt += fmt.Sprintf("### %s\n%s\n\n", result.ExpertName, result.Analysis)
			}
		}
		prompt += fmt.Sprintf("Manager 决策: %s\n\n", iteration.Decision.Decision)
	}

	prompt += `
请生成一份包含以下内容的报告：
1. 总结摘要
2. 根本原因
3. 业务影响
4. 性能分析
5. 建议措施
6. 处理优先级
7. 预计解决时间
`

	return prompt
}

// analyzeScenario 分析告警场景
func (m *ManagerAgent) analyzeScenario(alarms []Alarm) ScenarioInfo {
	// 统计告警级别
	criticalCount := 0
	warningCount := 0
	serviceMap := make(map[string]int)

	for _, alarm := range alarms {
		serviceMap[alarm.Service]++
		switch alarm.Level {
		case "critical":
			criticalCount++
		case "warning":
			warningCount++
		}
	}

	// 确定场景类型
	scenarioType := "性能问题"
	if criticalCount > 0 {
		scenarioType = "严重故障"
	}

	// 确定优先级
	priority := "P2"
	if criticalCount > 0 {
		priority = "P0"
	} else if warningCount >= 2 {
		priority = "P1"
	}

	// 估算轮次
	estimatedRounds := 2
	if len(alarms) > 3 || criticalCount > 0 {
		estimatedRounds = 3
	}

	// 重点领域
	focusAreas := []string{
		"支付接口性能",
		"数据库连接管理",
	}

	// 策略
	strategy := "采用多专家并行分析模式，每轮迭代综合各方意见，逐步缩小根因范围"

	return ScenarioInfo{
		Type:           scenarioType,
		Priority:       priority,
		EstimatedRounds: estimatedRounds,
		FocusAreas:     focusAreas,
		Strategy:       strategy,
	}
}

// AnalysisPlan 分析计划
type AnalysisPlan struct {
	Scenario        string   `json:"scenario"`
	Priority        string   `json:"priority"`
	EstimatedRounds int      `json:"estimatedRounds"`
	FocusAreas      []string `json:"focusAreas"`
	Strategy        string   `json:"strategy"`
}

// IterationResult 迭代结果
type IterationResult struct {
	Iteration     int                     `json:"iteration"`
	ExpertResults []*ExpertAnalysisResult `json:"expertResults"`
	Decision      *ManagerDecision        `json:"decision"`
	Timestamp     int64                   `json:"timestamp"`
}

// ScenarioInfo 场景信息
type ScenarioInfo struct {
	Type           string   `json:"type"`
	Priority       string   `json:"priority"`
	EstimatedRounds int      `json:"estimatedRounds"`
	FocusAreas     []string `json:"focusAreas"`
	Strategy       string   `json:"strategy"`
}
