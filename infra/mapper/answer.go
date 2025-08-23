package mapper

import (
	"context"
	"errors"
	"fmt"
	"github.com/zeromicro/go-zero/core/logx"
	"gitlab.aiecnu.net/elion/elion-reading-post/infra/config"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"sync"
	"time"
)

// 对应数据库中 elion_reading_question_student_answer 表
// 涉及录音的字段有
// audio 路径
// audio_time 录音时长
// audio_content_type 固定为MIME
// audio_status 0 未提交, 1 提交未批改, 2 批改中, 3 批改完成

type (
	Answer struct {
		ID               int       `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
		StudentID        string    `gorm:"column:student_id;size:255" json:"student_id"`
		QuestionID       string    `gorm:"column:question_id;size:255" json:"question_id"`
		AnswerID         string    `gorm:"column:answer_id;size:255" json:"answer_id"`
		AnswerText       string    `gorm:"column:answer;type:text" json:"answer"`
		IsCorrect        int8      `gorm:"column:is_correct" json:"is_correct"`
		SubmittedTime    time.Time `gorm:"column:submitted_time" json:"submitted_time"`
		Score            int       `gorm:"column:score" json:"score"`
		Comment          string    `gorm:"column:comment;type:text" json:"comment"`
		Audio            string    `gorm:"column:audio;type:text" json:"audio"`
		AudioTime        int       `gorm:"column:audio_time" json:"audio_time"`
		AudioContentType string    `gorm:"column:audio_content_type;size:255" json:"audio_content_type"`
		AudioStatus      int       `gorm:"column:audio_status" json:"audio_status"`
		HandleTime       time.Time `gorm:"column:handle_time" json:"handle_time"`
		Origin           string    // 原文 TODO 原文查询
	}
	FindOriginResult struct {
		QuestionId string `gorm:"column:question_id"`
		Origin     string `gorm:"column:content"`
	}
	AnswerMapper struct {
		db *gorm.DB
	}
)

const (
	UnHandIn  = 0
	UnHandled = 1
	Handling  = 2
	Handled   = 3
)

var (
	answerMapper      *AnswerMapper
	once              sync.Once
	UnHandledCond     = &Answer{AudioStatus: UnHandled}
	HandlingCond      = &Answer{AudioStatus: Handling}
	HandledCond       = &Answer{AudioStatus: Handled}
	NoOneFinished     = errors.New("没有记录被更新, 可能记录不存在或已完成")
	Question2Homework = "table_elion_reading_homework_question"
	Homework2Reading  = "table_elion_reading_homework"
	Reading2Text      = "table_elion_reading"
	Text2Origin       = "table_elion_reading_text"
)

// GetAnswerMapper 获取AnswerMapper单例
func GetAnswerMapper() *AnswerMapper {
	once.Do(func() {
		conf := config.GetConfig()
		db, err := gorm.Open(mysql.Open(conf.DB.DSN), &gorm.Config{})
		if err != nil {
			panic(err)
		}
		answerMapper = &AnswerMapper{db: db}
	})
	return answerMapper
}

// ListUnHandledAnswers 获取未处理的答案
func (m *AnswerMapper) ListUnHandledAnswers(ctx context.Context, size int) ([]*Answer, error) {
	var answers = make([]*Answer, 0)
	err := m.db.Transaction(func(tx *gorm.DB) (err error) {
		// 获取未处理的记录, 先处理提交早的
		find := tx.WithContext(ctx).Where(UnHandledCond).Where("audio IS NOT NULL AND audio != ''").Order("submitted_time ASC").Limit(size).Find(&answers)
		if find.Error != nil && !errors.Is(find.Error, gorm.ErrRecordNotFound) { // 查询失败
			return find.Error
		} else if len(answers) == 0 { // 未查询到
			return nil
		}

		// 更新查询到的记录
		var ids = make([]int, 0, len(answers))
		for _, answer := range answers {
			ids = append(ids, answer.ID)
		}

		// 将获取的记录都标记为处理中
		updates := tx.WithContext(ctx).Model(&Answer{}).Where("id IN ? AND audio_status = ?", ids, UnHandled).Updates(map[string]any{
			"audio_status": Handling,
			"handle_time":  time.Now(),
		})
		if updates.Error != nil {
			return updates.Error
		} else if updates.RowsAffected != int64(len(ids)) {
			return fmt.Errorf("获取到的未处理记录标记更新中失败")
		}
		// 收集问题id
		questionSet := make(map[string]struct{})
		var question []string
		for _, answer := range answers {
			questionSet[answer.QuestionID] = struct{}{}
		}
		for key := range questionSet {
			question = append(question, key)
		}
		// 根据questions_id查询homework_id, 根据homework_id查询reference_reading_id, 根据reference_reading_id查询原文
		var origins []FindOriginResult
		if err = tx.WithContext(ctx).Table(Question2Homework).
			Select(fmt.Sprintf("%s.question_id, %s.content", Question2Homework, Text2Origin)).
			Joins(fmt.Sprintf("JOIN %s ON %s.homework_id = %s.homework_id", Homework2Reading, Question2Homework, Homework2Reading)).
			Joins(fmt.Sprintf("JOIN %s ON %s.reference_reading_id = %s.reading_id", Reading2Text, Homework2Reading, Reading2Text)).
			Joins(fmt.Sprintf("JOIN %s ON %s.text_id = %s.text_id", Text2Origin, Reading2Text, Text2Origin)).
			Where(fmt.Sprintf("%s.question_id IN ?", Question2Homework), question). // optimize 课外的题目可能没有文本
			Scan(&origins).Error; err != nil {
			return err
		}
		question2Origin := make(map[string]string)
		for _, origin := range origins {
			question2Origin[origin.QuestionId] = origin.Origin
		}
		for _, answer := range answers {
			answer.Origin = question2Origin[answer.QuestionID]
		}
		return err
	})
	return answers, err
}

// FinishOne 将一个Handling的Answer标记为Handled
func (m *AnswerMapper) FinishOne(ctx context.Context, id int, comment string) (success bool, err error) {
	err = m.db.Transaction(func(tx *gorm.DB) (err error) {
		var ans Answer
		first := tx.WithContext(ctx).Model(&Answer{}).Where("id = ?", id).First(&ans)
		if first.Error != nil { // TODO 上游处理not found
			logx.Error("查询id:%d失败:%s", id, first.Error.Error())
			return err
		} else if ans.AudioStatus == Handled { // 已完成直接返回即可
			success = true
			return nil
		}

		// 更新处理中的记录为已完成, 并记录comment
		update := tx.Model(&Answer{}).Where("id = ? AND audio_status = ?", id, Handling).Updates(&Answer{
			AudioStatus: Handled,
			Comment:     comment,
			HandleTime:  time.Now(),
		})
		if update.Error != nil {
			logx.Error("更新id:%d失败:err", id, update.Error.Error())
			return update.Error
		} else if update.RowsAffected == 0 { // 更新失败, 可能是记录不存在或状态不是handling
			return NoOneFinished
		}
		success = true
		return nil
	})
	return success, err
}

func (m *AnswerMapper) Reset(ctx context.Context, exclude []int) (err error) {
	expire := time.Now().Add(time.Duration(config.GetConfig().Expire) * time.Second)
	db := m.db.WithContext(ctx).Model(&Answer{}).
		Where("audio_status = ?", Handling).
		Where("handle_time < ?", expire)

	if len(exclude) > 0 {
		db = db.Where("id NOT IN ?", exclude)
	}

	result := db.Updates(map[string]any{"audio_status": UnHandled, "handle_time": time.Now()})
	return result.Error
}

func (a Answer) TableName() string {
	return "table_elion_reading_question_student_answer"
}
