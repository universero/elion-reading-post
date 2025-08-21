package call

import (
	"errors"
	"fmt"
	"github.com/cloudwego/eino-ext/components/model/deepseek"
	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/schema"
	"github.com/zeromicro/go-zero/core/logx"
	"gitlab.aiecnu.net/elion/elion-reading-post/infra/config"
	"golang.org/x/net/context"
	"strings"
	"unicode/utf8"
)

func init() {
	var err error
	if commentModel, err = deepseek.NewChatModel(nil, &deepseek.ChatModelConfig{
		APIKey:  config.GetConfig().Comment.ApiKey,
		BaseURL: config.GetConfig().Comment.BaseURL,
	}); err != nil {
		panic("create comment model err:" + err.Error())
	}
}

var (
	// 评论提示词模板
	commentPrompt = prompt.FromMessages(schema.FString,
		schema.AssistantMessage(config.GetConfig().Comment.Assistant, nil),
		schema.UserMessage(config.GetConfig().Comment.Template))
	commentModel *deepseek.ChatModel
	NoReasoning  = errors.New("无有效内容")
)

// CommentTask 评价任务
type CommentTask struct {
	id      int
	origin  string // 原文
	reading string // 学生朗读
	resp    *schema.Message
}

// NewCommentTask 创建评价任务
func NewCommentTask(id int, origin, reading string) *CommentTask {
	return &CommentTask{id: id, origin: origin, reading: reading}
}

// Submit 提交评价任务
func (t *CommentTask) Submit() (ok bool, err error) {
	similarity := t.similarity() // 计算相似度
	var msgs []*schema.Message   // 构造提示词
	if msgs, err = commentPrompt.Format(context.Background(), formatInfos(t.origin, t.reading, similarity)); err != nil {
		return false, err
	}
	if t.resp, err = commentModel.Generate(context.Background(), msgs); err != nil {
		return false, err
	}
	return true, nil
}

// Query 获取评价结果
func (t *CommentTask) Query() (string, error) {
	if reasoning, ok := deepseek.GetReasoningContent(t.resp); ok {
		logx.Info("[comment task] id: %d comment success | Tokens used: %d (prompt) + %d (completion) = %d (total)",
			t.id, t.resp.ResponseMeta.Usage.PromptTokens, t.resp.ResponseMeta.Usage.CompletionTokens, t.resp.ResponseMeta.Usage.TotalTokens)
		return reasoning, nil
	}
	return "", NoReasoning
}

// similarity 计算朗读文本与原文的相似度
// 返回相似度百分比(0-100)和详细的错误分析
func (t *CommentTask) similarity() map[string]any {
	// 预处理文本：去除标点符号和空白字符，统一为小写（如果是英文）
	origin, reading := cleanText(t.origin), cleanText(t.reading)
	// 计算编辑距离
	distance := calculateEditDistance(origin, reading)
	maxLen := max(utf8.RuneCountInString(origin), utf8.RuneCountInString(reading))
	// 计算相似度百分比
	similarity := 100.0 * (1.0 - float64(distance)/float64(maxLen))
	// 错误分析
	e := analyzeErrors(origin, reading)
	return map[string]any{
		"相似度":  similarity,
		"编辑距离": distance,
		"错误分析": e,
		"原文长度": utf8.RuneCountInString(origin),
		"朗读长度": utf8.RuneCountInString(reading),
	}
}

// 中文常见标点符号集合
var (
	punctuations = map[rune]bool{
		'，': true, '。': true, '、': true, '；': true, '：': true,
		'？': true, '！': true, '「': true, '」': true, '『': true,
		'』': true, '（': true, '）': true, '【': true, '】': true,
		'《': true, '》': true, '〈': true, '〉': true, '“': true,
		'”': true, '‘': true, '’': true, '…': true, '—': true,
		'～': true, '·': true,
	}
	// 空白字符集合
	whitespaces = map[rune]bool{
		' ': true, '\t': true, '\n': true, '\r': true,
	}
)

// cleanText 清理文本，去除标点符号和空白
func cleanText(text string) string {
	return strings.Map(func(r rune) rune {
		if punctuations[r] || whitespaces[r] {
			return -1 // 删除标点和空白
		}
		return r
	}, text)
}

// calculateEditDistance 计算编辑距离（Levenshtein距离）
func calculateEditDistance(a, b string) int {
	runeA, runeB := []rune(a), []rune(b)
	lenA, lenB := len(runeA), len(runeB)

	// 创建二维数组存储编辑距离
	matrix := make([][]int, lenA+1)
	for i := 0; i <= lenA; i++ {
		matrix[i] = make([]int, lenB+1)
		matrix[i][0] = i
	}
	for j := 0; j <= lenB; j++ {
		matrix[0][j] = j
	}

	// 计算编辑距离
	for i := 1; i <= lenA; i++ {
		for j := 1; j <= lenB; j++ {
			cost := 0
			if runeA[i-1] != runeB[j-1] {
				cost = 1
			}
			matrix[i][j] = min(
				matrix[i-1][j]+1,      // 删除
				matrix[i][j-1]+1,      // 插入
				matrix[i-1][j-1]+cost, // 替换
			)
		}
	}
	return matrix[lenA][lenB]
}

// analyzeErrors 分析错误类型
func analyzeErrors(origin, reading string) map[string]int {
	runeO, runeR, e := []rune(origin), []rune(reading), make(map[string]int)

	// 使用动态规划回溯路径找出具体错误
	i, j := len(runeO), len(runeR)
	matrix := make([][]int, i+1)
	for x := 0; x <= i; x++ {
		matrix[x] = make([]int, j+1)
	}

	// 这里简化的错误分析, 只统计增删改的次数，后续可以使用更复杂的算法
	minLen := min(i, j)
	for k := 0; k < minLen; k++ {
		if runeO[k] != runeR[k] {
			e["替换错误"]++
		}
	}
	if i > j {
		e["遗漏内容"] += i - j
	} else if j > i {
		e["多余内容"] += j - i
	}
	return e
}

// 格式化信息以填充prompt模板
func formatInfos(origin, reading string, result map[string]any) map[string]any {
	var builder strings.Builder
	// 基本信息
	builder.WriteString(fmt.Sprintf("相似度: %.2f%%\n", result["相似度"]))
	builder.WriteString(fmt.Sprintf("编辑距离: %d\n", result["编辑距离"]))
	builder.WriteString(fmt.Sprintf("原文长度: %d 字符\n", result["原文长度"]))
	builder.WriteString(fmt.Sprintf("朗读长度: %d 字符\n\n", result["朗读长度"]))
	// 错误分析
	if e, ok := result["错误分析"].(map[string]int); ok {
		if len(e) == 0 {
			builder.WriteString("无错误\n")
		} else {
			for typ, v := range e {
				builder.WriteString(fmt.Sprintf("%s: %d 处\n", typ, v))
			}
		}
	}
	return map[string]any{"origin": origin, "reading": reading, "e": builder.String()}
}
