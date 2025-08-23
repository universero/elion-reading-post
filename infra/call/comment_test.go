package call

import (
	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/schema"
	"github.com/zeromicro/go-zero/core/logx"
	"golang.org/x/net/context"
	"testing"
)

func TestDeepseek(t *testing.T) {
	msgs, err := prompt.FromMessages(schema.FString, schema.UserMessage("你好")).Format(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var resp *schema.Message
	if resp, err = commentModel.Generate(context.Background(), msgs); err != nil {
		t.Fatal(err)
	}
	logx.Infof("[comment task]  comment success | Tokens used: %d (prompt) + %d (completion) = %d (total)",
		resp.ResponseMeta.Usage.PromptTokens, resp.ResponseMeta.Usage.CompletionTokens, resp.ResponseMeta.Usage.TotalTokens)
	if resp.Content != "" {
		t.Log(resp.Content)
		return
	}
	t.Fatal()
}
