# Agent Runtime 稳定性增强设计

## 执行摘要

当前系统在结构化输出失败时缺乏有效降级路径,导致合法请求在工具执行前失败。本方案通过**多层防御架构**,在保留结构化规划能力的同时,建立从规则修复到降级执行的完整容错链。

## 核心问题

### 1. 单点脆弱性
```
用户请求 → Planner → 验证 → [失败] → Repair → [失败] → 全局失败
                                  ↑
                          过度依赖 LLM 精准输出
```

### 2. 典型失败场景
- **Schema-less 工具链**: MCP 工具无 `output_schema`,planner 生成非法 binding
- **复杂条件分支**: 条件逻辑过于复杂,LLM 难以生成合法 JSON
- **预算冲突**: 工具预算设置不合理,无法满足约束
- **语义冲突**: 替换步骤与原步骤能力不兼容

### 3. 根本原因
1. **验证过于严格**: 在没有容错的情况下,严格验证导致合法需求被拒绝
2. **Repair 机制单一**: 只依赖 LLM 重试,没有基于规则的自动修复
3. **缺乏降级路径**: 无法部分执行或简化计划
4. **错误传播**: 一个步骤的问题影响整个执行

## 多层防御架构

```
层级 1: 智能 Planner (减少错误产生)
   ↓ [失败]
层级 2: 规则修复器 (自动修正已知问题)
   ↓ [失败]
层级 3: LLM Repair (处理未知模式)
   ↓ [失败]
层级 4: 计划简化器 (降级为更简单的执行模式)
   ↓ [失败]
层级 5: 应急执行器 (最小化功能保证)
```

### 层级 1: 智能 Planner

**增强 Prompt 工程**

```go
// plan/prompt_builder.go
type PromptContext struct {
    Tools        []model.ToolSpec
    UserGoal     string
    Constraints  []string
    Examples     []PlanExample
    PreviousErrors []ValidationError // 从历史学习
}

func BuildPlannerPrompt(ctx PromptContext) string {
    // 动态构建包含约束的 prompt
    rules := []string{
        "基础规则: 只返回可执行的 JSON,格式为 {version,steps}",
    }
    
    // 根据工具特性添加规则
    if hasSchemalessTools(ctx.Tools) {
        rules = append(rules, 
            "Schema-less 规则: output_schema 为 null 的工具不得作为 binding 来源",
            "替代方案: 使用 mode='agentic' 合并需要观察的工具链",
        )
    }
    
    // 添加负面示例(从之前的错误学习)
    if len(ctx.PreviousErrors) > 0 {
        rules = append(rules, fmt.Sprintf(
            "避免错误: %s", 
            summarizeErrors(ctx.PreviousErrors),
        ))
    }
    
    return buildFullPrompt(ctx.UserGoal, ctx.Tools, rules, ctx.Examples)
}
```

**工具分类与模式选择**

```go
// plan/tool_analyzer.go
type ToolPattern string

const (
    PatternDeterministic  ToolPattern = "deterministic"   // 有 schema,可用 binding
    PatternSchemaless     ToolPattern = "schemaless"      // 无 schema
    PatternChain          ToolPattern = "chain"           // 需串联观察
    PatternParallel       ToolPattern = "parallel"        // 可并行
)

type ToolChainAnalysis struct {
    Patterns      []ToolPattern
    Recommendation string  // "use_agentic" | "use_bindings" | "mixed"
    MaxComplexity int      // 预估难度
}

func AnalyzeToolChain(tools []model.ToolSpec, goal string) ToolChainAnalysis {
    // 静态分析工具能力
    patterns := []ToolPattern{}
    
    hasSchemaless := false
    for _, tool := range tools {
        if tool.OutputSchema == nil {
            hasSchemaless = true
            patterns = append(patterns, PatternSchemaless)
        }
    }
    
    // 根据模式推荐执行策略
    if hasSchemaless && needsChaining(tools, goal) {
        return ToolChainAnalysis{
            Patterns: patterns,
            Recommendation: "use_agentic",
            MaxComplexity: 2, // 降低复杂度
        }
    }
    
    return ToolChainAnalysis{
        Patterns: patterns,
        Recommendation: "use_bindings",
        MaxComplexity: 5,
    }
}
```

### 层级 2: 规则修复器

**自动修正已知问题模式**

```go
// plan/rule_fixer.go
type RuleFixer struct {
    Rules []FixRule
}

type FixRule interface {
    Match(p plan.Plan, err error) bool
    Apply(p plan.Plan, tools []model.ToolSpec) (plan.Plan, bool, error)
}

// 规则 1: Schema-less Binding 转 Agentic
type SchemalessBindingFix struct{}

func (f *SchemalessBindingFix) Match(p plan.Plan, err error) bool {
    return strings.Contains(err.Error(), "absent from") && 
           strings.Contains(err.Error(), "output schema")
}

func (f *SchemalessBindingFix) Apply(p plan.Plan, tools []model.ToolSpec) (plan.Plan, bool, error) {
    // 找到有问题的步骤
    problematicSteps := []plan.Step{}
    sourceSteps := map[string]plan.Step{}
    
    for _, step := range p.Steps {
        sourceSteps[step.ID] = step
    }
    
    for _, step := range p.Steps {
        for _, binding := range step.Bindings {
            source := sourceSteps[binding.SourceStep]
            tool, _ := findToolByCapability(tools, source.Capability)
            
            // 检测 schema-less binding
            if tool.OutputSchema == nil {
                problematicSteps = append(problematicSteps, source, step)
            }
        }
    }
    
    if len(problematicSteps) == 0 {
        return p, false, nil
    }
    
    // 合并为一个 agentic 步骤
    capabilities := []string{}
    for _, step := range problematicSteps {
        capabilities = append(capabilities, step.Capability)
    }
    
    agenticStep := plan.Step{
        ID:               generateID("agentic-fix"),
        Mode:             plan.StepModeAgentic,
        Goal:             extractGoal(problematicSteps),
        SuccessCriteria:  "根据工具观察完成任务,缺少信息时询问",
        Capabilities:     unique(capabilities),
        ToolBudget:       plan.ToolCallBudget{
            Min: 1,
            Max: min(len(capabilities)*2, 5),
            RequiredSuccesses: 1,
        },
    }
    
    // 替换原步骤
    newPlan := removeSteps(p, problematicSteps)
    newPlan.Steps = append(newPlan.Steps, agenticStep)
    newPlan.Version++
    
    return newPlan.Normalize()
}

// 规则 2: 预算冲突修复
type BudgetConflictFix struct{}

func (f *BudgetConflictFix) Match(p plan.Plan, err error) bool {
    return strings.Contains(err.Error(), "tool_budget") ||
           strings.Contains(err.Error(), "budget")
}

func (f *BudgetConflictFix) Apply(p plan.Plan, tools []model.ToolSpec) (plan.Plan, bool, error) {
    fixed := false
    for i := range p.Steps {
        if p.Steps[i].Mode == plan.StepModeAgentic {
            budget := &p.Steps[i].ToolBudget
            
            // 修正不合法的预算
            if budget.Min < 0 {
                budget.Min = 0
                fixed = true
            }
            if budget.Max < 1 {
                budget.Max = len(p.Steps[i].Capabilities) * 2
                fixed = true
            }
            if budget.Min > budget.Max {
                budget.Min = budget.Max / 2
                fixed = true
            }
            if budget.RequiredSuccesses > budget.Max {
                budget.RequiredSuccesses = budget.Max
                fixed = true
            }
        }
    }
    
    if fixed {
        p.Version++
        return p.Normalize()
    }
    return p, false, nil
}

// 规则 3: 循环依赖修复
type CircularDependencyFix struct{}

func (f *CircularDependencyFix) Match(p plan.Plan, err error) bool {
    return strings.Contains(err.Error(), "cycle") ||
           strings.Contains(err.Error(), "circular")
}

func (f *CircularDependencyFix) Apply(p plan.Plan, tools []model.ToolSpec) (plan.Plan, bool, error) {
    // 拓扑排序找环
    sorted, hasCycle := topologicalSort(p.Steps)
    if !hasCycle {
        return p, false, nil
    }
    
    // 打破环:移除最弱的依赖
    p.Steps = removeWeakestDependency(sorted)
    p.Version++
    return p.Normalize()
}
```

**规则修复器集成**

```go
// plan/planner.go
func (p *ModelPlanner) PlanWithAutoFix(ctx context.Context, input string, tools []model.ToolSpec) (plan.Plan, error) {
    // 第一次尝试
    result, err := p.Plan(ctx, input, tools)
    if err == nil {
        return result, nil
    }
    
    // 应用规则修复
    fixer := &RuleFixer{
        Rules: []FixRule{
            &SchemalessBindingFix{},
            &BudgetConflictFix{},
            &CircularDependencyFix{},
        },
    }
    
    for _, rule := range fixer.Rules {
        if rule.Match(result, err) {
            fixed, applied, fixErr := rule.Apply(result, tools)
            if fixErr != nil {
                continue
            }
            if applied {
                // 验证修复后的计划
                if validateErr := fixed.Validate(); validateErr == nil {
                    if validateErr := plan.ValidateAgainstTools(fixed, tools); validateErr == nil {
                        p.trace("rule_fixer.success", map[string]any{
                            "rule": fmt.Sprintf("%T", rule),
                            "original_error": err.Error(),
                        })
                        return fixed, nil
                    }
                }
            }
        }
    }
    
    // 规则修复失败,继续 LLM repair
    return result, err
}
```

### 层级 3: 增强 LLM Repair

**上下文增强的 Repair**

```go
// plan/enhanced_repair.go
func BuildRepairPrompt(original plan.Plan, validationErr error, tools []model.ToolSpec) string {
    // 分析错误类型
    errorType := classifyError(validationErr)
    
    prompt := "修复计划错误。原始错误:\n" + validationErr.Error() + "\n\n"
    
    // 根据错误类型提供针对性指导
    switch errorType {
    case "schema_missing":
        prompt += `
Schema 缺失修复指南:
1. 检查工具目录中哪些工具的 output_schema 为 null
2. 不得为这些工具创建 binding 的 source_path
3. 如果需要串联,使用 mode: "agentic":
   {
     "id": "combined",
     "mode": "agentic",
     "goal": "明确的目标",
     "success_criteria": "成功标准",
     "capabilities": ["tool1", "tool2"],
     "tool_budget": {"min": 1, "max": 3, "required_successes": 1}
   }
`
    case "budget_invalid":
        prompt += `
预算修复指南:
1. 确保 0 <= min <= max
2. 确保 0 <= required_successes <= max
3. max 必须 >= 1
4. 为 agentic 步骤设置合理上限(通常 2-5 次调用)
`
    case "binding_invalid":
        prompt += `
Binding 修复指南:
1. source_path 必须存在于源工具的 output_schema 中
2. target_path 必须存在于目标工具的 input_schema 中
3. 如果源工具 output_schema 为 null,删除 binding,改用 agentic 模式
`
    }
    
    // 附加工具详情
    prompt += "\n可用工具详情:\n" + formatToolDetails(tools)
    
    return prompt
}
```

### 层级 4: 计划简化器

**降级策略**

```go
// plan/simplifier.go
type Simplifier struct {
    MaxAgenticTools int
    AllowPartialExecution bool
}

func (s *Simplifier) SimplifyPlan(p plan.Plan, tools []model.ToolSpec) (plan.Plan, error) {
    // 策略 1: 移除可选步骤
    essentialSteps := filterEssentialSteps(p.Steps)
    
    // 策略 2: 将所有步骤合并为一个 agentic
    if len(essentialSteps) > 1 && s.MaxAgenticTools > 0 {
        return s.mergeToAgentic(p, essentialSteps, tools)
    }
    
    // 策略 3: 只执行第一个可行步骤
    if s.AllowPartialExecution && len(essentialSteps) > 0 {
        return plan.Plan{
            ID:      p.ID,
            Goal:    p.Goal,
            Version: p.Version + 1,
            Steps:   []plan.Step{essentialSteps[0]},
        }, nil
    }
    
    return p, fmt.Errorf("cannot simplify plan further")
}

func (s *Simplifier) mergeToAgentic(p plan.Plan, steps []plan.Step, tools []model.ToolSpec) (plan.Plan, error) {
    capabilities := []string{}
    for _, step := range steps {
        if step.Mode == plan.StepModeAgentic {
            capabilities = append(capabilities, step.Capabilities...)
        } else {
            capabilities = append(capabilities, step.Capability)
        }
    }
    
    // 限制工具数量
    if len(capabilities) > s.MaxAgenticTools {
        capabilities = capabilities[:s.MaxAgenticTools]
    }
    
    unified := plan.Step{
        ID:   "simplified-agentic",
        Mode: plan.StepModeAgentic,
        Goal: p.Goal,
        SuccessCriteria: "尽可能完成任务,无法完成时说明原因",
        Capabilities: unique(capabilities),
        ToolBudget: plan.ToolCallBudget{
            Min: 0,
            Max: min(len(capabilities)*2, s.MaxAgenticTools*2),
            RequiredSuccesses: 0, // 允许部分成功
        },
    }
    
    return plan.Plan{
        ID:      p.ID,
        Goal:    p.Goal,
        Version: p.Version + 1,
        Steps:   []plan.Step{unified},
    }, nil
}
```

### 层级 5: 应急执行器

**无计划直接执行**

```go
// plan/emergency.go
type EmergencyExecutor struct {
    AllowedTools []string
    MaxAttempts  int
}

func (e *EmergencyExecutor) Execute(ctx context.Context, userGoal string, tools []model.ToolSpec, model model.Model) (*Response, error) {
    // 创建最简单的 ReACT 循环
    runner := react.Runner{
        Model:        model,
        Registry:     buildRegistry(tools, e.AllowedTools),
        DisableTools: len(e.AllowedTools) == 0,
        Budget: react.Budget{
            MaxModelCalls: e.MaxAttempts,
            MaxIterations: e.MaxAttempts,
        },
    }
    
    systemPrompt := `你是一个助手。
如果有可用工具,使用它们获取信息。
如果没有工具或工具无法完成任务,根据已知信息回答或说明无法完成。
不要编造信息。`
    
    messages := []model.Message{
        {Role: model.RoleSystem, Content: systemPrompt},
        {Role: model.RoleUser, Content: userGoal},
    }
    
    result, err := runner.RunInput(ctx, model.ModelInput{Messages: messages})
    if err != nil {
        return nil, err
    }
    
    return &Response{
        Text:     result.Text,
        Outcome:  contract.OutcomeDegraded,
        Warnings: []contract.Warning{{
            Code:    "EMERGENCY_EXECUTION",
            Message: "计划生成失败,使用应急执行模式",
        }},
    }, nil
}
```

## 配置与控制

```go
// config.go
type StabilityConfig struct {
    // 启用哪些层级
    EnableRuleFixer      bool
    EnableSimplifier     bool
    EnableEmergency      bool
    
    // 每层重试次数
    RuleFixerAttempts    int
    LLMRepairAttempts    int
    SimplificationLevels int
    
    // 降级控制
    AllowPartialSuccess  bool
    AllowDegradedMode    bool
    
    // 预算控制
    MaxAgenticTools      int
    EmergencyToolLimit   int
}

func DefaultStabilityConfig() StabilityConfig {
    return StabilityConfig{
        EnableRuleFixer:      true,
        EnableSimplifier:     true,
        EnableEmergency:      true,
        RuleFixerAttempts:    1,
        LLMRepairAttempts:    1,  // 降低到1次
        SimplificationLevels: 2,
        AllowPartialSuccess:  true,
        AllowDegradedMode:    true,
        MaxAgenticTools:      5,
        EmergencyToolLimit:   3,
    }
}

func WithStability(config StabilityConfig) Option {
    return func(c *config) error {
        c.stability = config
        return nil
    }
}
```

## 可观测性增强

```go
// observe/stability_events.go
const (
    EventRuleFixerAttempt  = "stability.rule_fixer.attempt"
    EventRuleFixerSuccess  = "stability.rule_fixer.success"
    EventSimplificationStart = "stability.simplification.start"
    EventSimplificationSuccess = "stability.simplification.success"
    EventEmergencyExecution = "stability.emergency.start"
    EventDegradedSuccess    = "stability.degraded.success"
)

type StabilityMetrics struct {
    TotalRequests       int64
    PlannerSuccesses    int64
    RuleFixerUsages     int64
    SimplifierUsages    int64
    EmergencyUsages     int64
    FullSuccessRate     float64
    DegradedSuccessRate float64
}
```

## 完整执行流程

```go
// agent.go - 集成所有层级
func (r *runtime) RunWithStability(ctx context.Context, req Request, opts ...RunOption) (*Response, error) {
    tools := r.registry.ModelSpecs()
    
    // 层级 1: 智能 Planner
    promptCtx := PromptContext{
        Tools:    tools,
        UserGoal: req.Input,
    }
    enhancedPrompt := BuildPlannerPrompt(promptCtx)
    
    planner := &ModelPlanner{
        Model:  r.cfg.model,
        Prompt: enhancedPrompt,
    }
    
    initialPlan, planErr := planner.Plan(ctx, req.Input, tools)
    
    // 层级 2: 规则修复
    if planErr != nil {
        ruleFixer := &RuleFixer{
            Rules: []FixRule{
                &SchemalessBindingFix{},
                &BudgetConflictFix{},
                &CircularDependencyFix{},
            },
        }
        
        for _, rule := range ruleFixer.Rules {
            if rule.Match(initialPlan, planErr) {
                fixed, applied, fixErr := rule.Apply(initialPlan, tools)
                if fixErr == nil && applied {
                    if err := fixed.Validate(); err == nil {
                        initialPlan = fixed
                        planErr = nil
                        break
                    }
                }
            }
        }
    }
    
    // 层级 3: LLM Repair (使用增强 prompt)
    if planErr != nil {
        var invalid *plan.StructuredOutputError
        if errors.As(planErr, &invalid) && invalid.Recoverable {
            repairPrompt := BuildRepairPrompt(initialPlan, planErr, tools)
            replanner := &ModelReplanner{
                Model:  r.cfg.model,
                Prompt: repairPrompt,
            }
            
            patch, repairErr := replanner.Replan(ctx, plan.Input{
                Original: initialPlan,
                Failure:  planErr.Error(),
                Tools:    tools,
            })
            
            if repairErr == nil {
                initialPlan, planErr = patch.Apply(initialPlan)
            }
        }
    }
    
    // 层级 4: 计划简化
    if planErr != nil {
        simplifier := &Simplifier{
            MaxAgenticTools:       5,
            AllowPartialExecution: true,
        }
        
        simplified, simplifyErr := simplifier.SimplifyPlan(initialPlan, tools)
        if simplifyErr == nil {
            initialPlan = simplified
            planErr = nil
        }
    }
    
    // 层级 5: 应急执行
    if planErr != nil {
        emergency := &EmergencyExecutor{
            AllowedTools: extractToolNames(tools),
            MaxAttempts:  3,
        }
        
        return emergency.Execute(ctx, req.Input, tools, r.cfg.model)
    }
    
    // 正常执行计划
    return r.executePlan(ctx, initialPlan, req, opts...)
}

## 配置与控制

```go
// config.go
type StabilityConfig struct {
    // 启用哪些层级
    EnableRuleFixer      bool
    EnableSimplifier     bool
    EnableEmergency      bool
    
    // 每层重试次数
    RuleFixerAttempts    int
    LLMRepairAttempts    int
    SimplificationLevels int
    
    // 降级控制
    AllowPartialSuccess  bool
    AllowDegradedMode    bool
    
    // 预算控制
    MaxAgenticTools      int
    EmergencyToolLimit   int
}

func DefaultStabilityConfig() StabilityConfig {
    return StabilityConfig{
        EnableRuleFixer:      true,
        EnableSimplifier:     true,
        EnableEmergency:      true,
        RuleFixerAttempts:    1,
        LLMRepairAttempts:    1,
        SimplificationLevels: 2,
        AllowPartialSuccess:  true,
        AllowDegradedMode:    true,
        MaxAgenticTools:      5,
        EmergencyToolLimit:   3,
    }
}
```

## 实施计划

### 阶段 1: 基础设施 (1-2 天)

**创建核心组件:**
1. `plan/rule_fixer.go` - 规则修复框架
2. `plan/fixes/schemaless.go` - Schema-less 修复
3. `plan/fixes/budget.go` - 预算修复
4. `plan/prompt_builder.go` - 动态 Prompt 构建

**验收**: Schema-less 案例通过,规则修复成功率 >70%

### 阶段 2: 降级策略 (1-2 天)

**创建降级组件:**
1. `plan/simplifier.go` - 计划简化器
2. `plan/emergency.go` - 应急执行器
3. 集成到 `agent.go` 主流程

**验收**: 整体成功率(含降级) >95%

### 阶段 3: 集成测试 (1 天)

**测试覆盖:**
- Schema-less 工具链
- 复杂预算约束
- 循环依赖
- 端到端集成

**验收**: 所有测试通过,性能开销 <10%

## 成功指标

| 指标 | 当前 | 目标 |
|------|------|------|
| 整体成功率 | ~60% | >95% |
| 初次规划成功率 | ~40% | >60% |
| 降级执行率 | N/A | <15% |
| P95 延迟增量 | - | <20% |

## 总结

这个多层防御架构将系统从"脆弱的精准依赖"转变为"稳健的容错系统",在保留复杂任务编排能力的同时,大幅提升整体成功率。

核心优势:
- **预防优先**: 智能 Prompt 减少错误产生
- **快速修复**: 规则修复器处理已知模式
- **必然兜底**: 降级路径保证基本可用
- **渐进部署**: 分阶段灰度降低风险
