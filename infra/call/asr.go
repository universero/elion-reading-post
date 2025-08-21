package call

import (
	"errors"
	"github.com/avast/retry-go"
	"github.com/zeromicro/go-zero/core/logx"
	"gitlab.aiecnu.net/elion/elion-reading-post/infra/config"
	"net/http"
	"time"
)

var (
	submitEndpoint = "https://openspeech.bytedance.com/api/v3/auc/bigmodel/submit"
	queryEndpoint  = "https://openspeech.bytedance.com/api/v3/auc/bigmodel/query"
	modelName      = "bigmodel"
	modelVersion   = "400"
	opts           = []retry.Option{ // 重试策略
		retry.Attempts(uint(5)),             // 最大重试次数
		retry.DelayType(retry.BackOffDelay), // 指数退避策略
		retry.MaxDelay(16 * time.Second),    // 最大退避间隔
		retry.OnRetry(func(n uint, err error) { // 重试日志
			logx.Info("[asr file task] retry #%d times with err:%v", n+1, err)
		})}
	ASRTaskFailed = errors.New("[asr file task] task failed") // 任务失败
	queryInterval = 3                                         // 每三秒查询一次
)

type (
	// Submit 提交请求
	Submit struct {
		User         SubmitUser `json:"user"`
		Audio        Audio      `json:"audio"`
		Request      Request    `json:"request"`
		Callback     string     `json:"callback,omitempty"`
		CallbackData string     `json:"callback_data,omitempty"`
	}
	SubmitUser struct {
		UID string `json:"uid"` // 用户标识
	}
	Audio struct {
		URL     string `json:"url"`               // 音频链接
		Format  string `json:"format"`            // 音频容器格式: raw/wav/mp3/ogg
		Codec   string `json:"codec,omitempty"`   // 音频编码格式: raw/opus
		Rate    int    `json:"rate,omitempty"`    // 音频采样率
		Bits    int    `json:"bits,omitempty"`    // 音频采样点位数
		Channel int    `json:"channel,omitempty"` // 音频声道数
	}
	Request struct {
		ModelName              string `json:"model_name"`                         // 模型名称
		ModelVersion           string `json:"model_version,omitempty"`            // 模型版本
		EnableITN              bool   `json:"enable_itn,omitempty"`               // 启用ITN
		EnablePunc             bool   `json:"enable_punc,omitempty"`              // 启用标点
		EnableDDC              bool   `json:"enable_ddc,omitempty"`               // 启用顺滑
		EnableSpeakerInfo      bool   `json:"enable_speaker_info,omitempty"`      // 启用说话人聚类分离
		EnableChannelSplit     bool   `json:"enable_channel_split,omitempty"`     // 启用双声道识别
		ShowUtterances         bool   `json:"show_utterances,omitempty"`          // 输出语音停顿、分句、分词信息
		ShowSpeechRate         bool   `json:"show_speech_rate,omitempty"`         // 分句信息携带语速
		ShowVolume             bool   `json:"show_volume,omitempty"`              // 分句信息携带音量
		EnableLID              bool   `json:"enable_lid,omitempty"`               // 启用语种识别
		EnableEmotionDetection bool   `json:"enable_emotion_detection,omitempty"` // 启用情绪检测
		EnableGenderDetection  bool   `json:"enable_gender_detection,omitempty"`  // 启用性别检测
		VadSegment             bool   `json:"vad_segment,omitempty"`              // 使用vad分句
		EndWindowSize          int    `json:"end_window_size,omitempty"`          // 强制判停时间
		SensitiveWordsFilter   string `json:"sensitive_words_filter,omitempty"`   // 敏感词过滤
		Corpus                 string `json:"corpus,omitempty"`                   // 语料/干预词等
		BoostingTableName      string `json:"boosting_table_name,omitempty"`      // 热词词表名称
		Context                string `json:"context,omitempty"`                  // 上下文功能
	}
	// ASRTaskResp 识别结果
	ASRTaskResp struct {
		Result Result `json:"result,omitempty"` // 识别结果，仅当识别成功时填写
	}
	Result struct {
		Text       string      `json:"text,omitempty"`       // 整个音频的识别结果文本
		Utterances []Utterance `json:"utterances,omitempty"` // 识别结果语音分句信息
	}
	Utterance struct {
		Text      string `json:"text,omitempty"`       // utterance级的文本内容
		StartTime int    `json:"start_time,omitempty"` // 起始时间(毫秒)
		EndTime   int    `json:"end_time,omitempty"`   // 结束时间(毫秒)
	}
	// FileAsrTask 识别任务
	FileAsrTask struct {
		Uid     string `json:"uid"`
		Url     string `json:"url"`
		Format  string `json:"format"`
		Codec   string `json:"codec"`
		Rate    int    `json:"rate"`
		Bits    int    `json:"bits"`
		Channel int    `json:"channel"`
	}
)

// NewFileAsrTask 创建一个文件ASR任务
func NewFileAsrTask(uid, url, format, codec string, rate, bits, channel int) *FileAsrTask {
	return &FileAsrTask{
		Uid:     uid,
		Url:     url,
		Format:  format,
		Codec:   codec,
		Rate:    rate,
		Bits:    bits,
		Channel: channel,
	}
}

// Submit 提交一个任务
func (t *FileAsrTask) Submit() (bool, error) {
	var header http.Header
	err := retry.Do(func() (err error) {
		if header, _, err = GetHttpClient().PostWithHeader(submitEndpoint, t.buildHeader(), t.buildSubmit()); err != nil {
			logx.Error("[asr file task]: post err: %s", err)
			return err
		}
		logx.Info("[asr file task]: log id: %s, status: code %s, message: %s", header.Get("X-Tt-Logid"), header.Get("X-Api-Status-Code"), header.Get("X-Api-Message"))
		return nil
	}, opts...)
	return IsASRSuccess(header.Get("X-Api-Status-Code")), err
}

// IsASRSuccess 判断是否提交成功
func IsASRSuccess(code string) bool {
	return code == "20000000"
}

// Query 查询结果
func (t *FileAsrTask) Query() (*ASRTaskResp, error) {
	query := t.buildHeader()
	for {
		var header http.Header
		var body map[string]any
		if err := retry.Do(func() (err error) { // 单次请求失败重试
			if header, body, err = GetHttpClient().PostWithHeader(queryEndpoint, query, nil); err != nil {
				logx.Error("[asr file task] query http err: %s", err)
			}
			return err
		}, opts...); err != nil { // 多次请求失败, 可能出现网络问题, 退出
			logx.Error("[asr file task]: query retry too many times err: %s", err)
			return nil, err
		}
		code := header.Get("X-Api-Status-Code")
		if IsASRSuccess(code) { // 成功
			return conv2ASRTaskResp(body), nil
		} else if !t.IsReQuery(code) { // ASR任务失败
			return nil, ASRTaskFailed
		}
		time.Sleep(time.Duration(queryInterval) * time.Second)
	}
}

// IsReQuery 是否需要重新查询
func (t *FileAsrTask) IsReQuery(code string) bool {
	return code == "20000001" || code == "20000002"
}

func (t *FileAsrTask) buildSubmit() *Submit {
	return &Submit{
		User: SubmitUser{
			UID: t.Uid,
		},
		Audio: Audio{
			URL:     t.Url,
			Format:  t.Format,
			Codec:   t.Codec,
			Rate:    t.Rate,
			Bits:    t.Bits,
			Channel: t.Channel,
		},
		Request: Request{
			ModelName:    modelName,
			ModelVersion: modelVersion,
		},
	}
}

func (t *FileAsrTask) buildHeader() http.Header {
	return http.Header{
		"X-Api-App-Key":      []string{config.GetConfig().ASR.AppKey},
		"X-Api-Access-Token": []string{config.GetConfig().ASR.AccessKey},
		"X-Api-Resource-Id":  []string{"volc.bigasr.auc"},
		"X-Api-Request-Id":   []string{t.Uid},
		"X-Api-Sequence":     []string{"-1"},
	}
}

func conv2ASRTaskResp(body map[string]any) *ASRTaskResp {
	var resp ASRTaskResp
	// 处理result字段
	if resultVal, ok := body["result"]; ok {
		if resultMap, ok := resultVal.(map[string]any); ok {
			// 处理text字段
			if text, ok := resultMap["text"].(string); ok {
				resp.Result.Text = text
			}
			// 处理utterances字段
			if utterancesVal, ok := resultMap["utterances"]; ok {
				if utterancesSlice, ok := utterancesVal.([]any); ok {
					resp.Result.Utterances = make([]Utterance, 0, len(utterancesSlice))
					for _, utteranceVal := range utterancesSlice {
						if utteranceMap, ok := utteranceVal.(map[string]any); ok {
							utterance := Utterance{}
							// 处理utterance的text字段
							if text, ok := utteranceMap["text"].(string); ok {
								utterance.Text = text
							}
							// 处理start_time字段
							if startTime, ok := utteranceMap["start_time"].(float64); ok {
								utterance.StartTime = int(startTime)
							}
							// 处理end_time字段
							if endTime, ok := utteranceMap["end_time"].(float64); ok {
								utterance.EndTime = int(endTime)
							}
							resp.Result.Utterances = append(resp.Result.Utterances, utterance)
						}
					}
				}
			}
		}
	}
	return &resp
}
