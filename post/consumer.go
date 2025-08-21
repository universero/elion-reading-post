package post

import (
	"fmt"
	"github.com/zeromicro/go-zero/core/logx"
	"gitlab.aiecnu.net/elion/elion-reading-post/infra/call"
	"time"
)

var (
	format  = "raw" // TODO 需要确定音频模式
	codec   = "raw"
	rate    = 16000
	bits    = 16
	channel = 1
)

type (
	Consumer struct {
		Manager *Manager
		Entry   *Entry
		ASRResp *call.ASRTaskResp // ASR结果
		Comment string            // 最终评价
	}
)

func NewConsumer(m *Manager) *Consumer {
	return &Consumer{Manager: m}
}

// Consume 开始消费
func (c *Consumer) Consume() {
	go c.consume()
}

// consume 实际消费逻辑
func (c *Consumer) consume() {
	for {
		// 请求新的
		c.Entry = c.Manager.RequestOne()
		// asr识别 与 生成评价, 一个失败就会放弃任务
		if !(c.asr() && c.comment() && c.finish()) {
			c.Manager.Abandon(c.Entry.ID) // TODO 放弃任务
		}
	}
}

// asr 识别文件
func (c *Consumer) asr() bool {
	if v, ok := c.Manager.QueryASR(c.Entry.ID); ok { // 先前处理过asr
		logx.Info("[consumer] asr hit cache %d", c.Entry.ID)
		c.ASRResp = v
		return true
	}

	var err error
	var ok bool
	task := call.NewFileAsrTask(c.uid(), c.Entry.Answer.Audio, format, codec, rate, bits, channel)
	if ok, err = task.Submit(); err != nil || !ok { // 提交失败
		logx.Error("[consumer] asr submit err:%s", err)
		return ok
	}
	if c.ASRResp, err = task.Query(); err != nil { // 查询失败
		logx.Error("[consumer] asr query err:%s", err)
		return false
	}

	// 缓存结果
	c.Manager.CacheASR(c.Entry.ID, c.ASRResp)
	return true
}

// comment 生成评语
func (c *Consumer) comment() bool {
	if v, ok := c.Manager.QueryCache(c.Entry.ID); ok {
		logx.Info("[consumer] comment hit cache %d", c.Entry.ID)
		c.Comment = v
		return true
	}

	var err error
	var ok bool
	task := call.NewCommentTask()
	if ok, err = task.Submit(); err != nil || !ok {
		logx.Error("[consumer] comment submit err:%s", err)
		return ok
	}
	if c.Comment, err = task.Query(); err != nil {
		logx.Error("[consumer] comment query err:%s", err)
		return false
	}
	return true
}

// finish 标记任务完成
func (c *Consumer) finish() bool {
	finished, err := c.Manager.FinishOne(c.Entry.ID, c.Comment)
	return err == nil && finished
}

func (c *Consumer) uid() string {
	return fmt.Sprintf("%s-%d", time.Now().Format("20060102-150405"), c.Entry.ID)
}
