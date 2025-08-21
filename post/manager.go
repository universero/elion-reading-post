package post

import (
	"errors"
	"github.com/avast/retry-go"
	"github.com/zeromicro/go-zero/core/logx"
	"gitlab.aiecnu.net/elion/elion-reading-post/infra/call"
	"gitlab.aiecnu.net/elion/elion-reading-post/infra/mapper"
	"golang.org/x/net/context"
	"golang.org/x/sync/singleflight"
	"sync"
	"time"
)

type (
	ConsumeState string // 消费状态
	// Manager 管理 Consumer的创建与销毁 TODO 多次失败的任务
	Manager struct {
		mu        sync.Mutex                // 维护manager状态
		fetchCond *sync.Cond                // 只有一个线程实际查数据库
		mapper    *mapper.AnswerMapper      // 数据库mapper
		cap       int                       // 消费者数量
		consumers []*Consumer               // 消费者
		idle      map[int]*Entry            // idle的Entry
		consuming map[int]*Entry            // 消费中的Entry
		abandon   map[int]*Entry            // 放弃的Entry
		cache     map[int]string            // 缓存id对应的comment
		asr       map[int]*call.ASRTaskResp // 缓存id对应的asr结果
		sf        singleflight.Group
	}
	Entry struct {
		ID           int            // 记录ID
		State        ConsumeState   // 记录状态
		Answer       *mapper.Answer // 记录信息
		AbandonTimes int            // 放弃次数
	}
)

var (
	Idle          ConsumeState = "idle"                     // 空闲
	Consuming     ConsumeState = "consuming"                // 消费中
	Finished      ConsumeState = "finished"                 // 已完成
	Abandoned     ConsumeState = "abandoned"                // 放弃
	NeedToWait                 = errors.New("暂时无新记录, 需要等待") // 标识等待的异常
	batch                      = 10                         // 一次取出的个数
	fetchInterval              = 60                         // fetch间隔
	maxAbandon                 = 5                          // 最多放弃五次
	opts                       = []retry.Option{            // 重试策略
		retry.Attempts(uint(5)),             // 最大重试次数
		retry.DelayType(retry.BackOffDelay), // 指数退避策略
		retry.MaxDelay(64 * time.Second),    // 最大退避间隔
		retry.OnRetry(func(n uint, err error) { // 重试日志
			logx.Info("[manager] retry #%d times with err:%v", n+1, err)
		}),
		retry.RetryIf(func(err error) bool {
			return !errors.Is(err, NeedToWait) // 当error不为NeedToWait时才需要重试
		}),
	}
	once    sync.Once
	manager *Manager
)

// GetManager 创建管理者
func GetManager(cap int) *Manager {
	once.Do(func() {
		m := &Manager{mu: sync.Mutex{}, fetchCond: sync.NewCond(&sync.Mutex{}), cap: cap, mapper: mapper.GetAnswerMapper()}
		for range cap {
			m.consumers = append(m.consumers, NewConsumer(m))
		}
		manager = m
	})
	return manager
}

// Run 启动所有的消费者
func (m *Manager) Run() {
	for _, c := range m.consumers {
		c.Consume()
	}
}

// RequestOne 消费者通过这个获取一个未消费的记录
func (m *Manager) RequestOne() (en *Entry) {
	for en = m.oneIdle(); en == nil; {
		m.fetchNewBatch() // 等待从数据库中获取
	}
	return en
}

// oneIdle 分配一个idle的Entry
func (m *Manager) oneIdle() *Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, v := range m.idle {
		v.Consuming(m)
		return v
	}
	return nil
}

// fetchNewBatch 获取新的批次, 如果获取失败会一直重试直到获取到
// 使用single flight确保同一时间只会有一个fetch任务
// 无错情况下. 每分钟重试一次. 有错情况下重试时间每次增加一分钟, 最大十分钟
func (m *Manager) fetchNewBatch() {
	if _, err, _ := m.sf.Do("fetchNewBatch", func() (any, error) {
		var cnt int
		for err := retry.Do(m.fetch, opts...); err != nil; {
			if errors.Is(err, NeedToWait) { // 需要等待
				time.Sleep(time.Duration(fetchInterval) * time.Second)
				continue
			} else if err != nil { // 出现错误
				logx.Error("[manager] fetchNewBatch err:%v", err)
				if cnt < 10 {
					cnt = cnt + 1
				}
				time.Sleep(time.Duration(fetchInterval*cnt) * time.Second)
				continue
			}
		}
		return nil, nil
	}); err != nil {
		logx.Error("[manager] fetchNewBatch err:%v", err)
	}
	return
}

// fetch 从数据库中查询一个batch并存储到idle中
func (m *Manager) fetch() error {
	// 从数据库中查询batch个
	ans, err := m.mapper.ListUnHandledAnswers(context.Background(), batch)
	if err != nil { // 查询失败
		logx.Error("[manager] fetch err: %s", err.Error())
		return err
	}

	if len(ans) == 0 { // 无新记录, 等待
		return NeedToWait
	} else { // 创建Entry并存入Idle
		m.mu.Lock()
		defer m.mu.Unlock()
		for _, v := range ans {
			NewEntry(v).Idle(m)
		}
		return nil
	}
}

// FinishOne 完成一个任务
func (m *Manager) FinishOne(id int, comment string) (success bool, err error) {
	// 判断是否被处理过
	en, ok := m.QueryConsuming(id)
	if !ok { // consuming 中不存在, 被处理过了
		return true, nil
	}

	// 缓存结果
	m.CacheOne(id, comment)
	success, err = m.mapper.FinishOne(context.Background(), id, comment)
	if success { // 完成成功
		m.RemoveCache(id) // 删除缓存
		m.RemoveASR(id)   // 删除asr缓存
		en.Finished(m)    // 移除任务
		return
	}
	en.Idle(m) // 完成失败, 将任务重新标记为未处理
	return
}

// Abandon 放弃一个任务
func (m *Manager) Abandon(id int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// 判断是否处理中
	en, ok := m.QueryConsuming(id)
	if !ok { // consuming 中不存在, 被处理过了
		return
	}

	if en.AbandonTimes >= maxAbandon { // 被放弃太多次了, 移入放弃中
		en.Abandon(m)
		return
	}
	en.AbandonTimes++
	en.Idle(m)
}

func (m *Manager) Unabandon(id int) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	en, ok := m.QueryAbandon(id)
	if !ok {
		return "no such id entry in abandon"
	}
	en.Unabandon(m)
	return "success"
}

// CacheOne 缓存一个id的处理结果
func (m *Manager) CacheOne(id int, comment string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cache[id] = comment
}

// QueryCache 查询这个id的结果是否有过缓存
func (m *Manager) QueryCache(id int) (v string, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok = m.cache[id]
	return
}

// RemoveCache 删除id对应缓存
func (m *Manager) RemoveCache(id int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cache, id)
}

// CacheASR 缓存一个id的ASR处理结果
func (m *Manager) CacheASR(id int, v *call.ASRTaskResp) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.asr[id] = v
}

// QueryASR 查询这个id的结果是否有过缓存
func (m *Manager) QueryASR(id int) (v *call.ASRTaskResp, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok = m.asr[id]
	return
}

// RemoveASR 删除id对应asr缓存
func (m *Manager) RemoveASR(id int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.asr, id)
}

// QueryIdle 查询一个Entry
func (m *Manager) QueryIdle(id int) (v *Entry, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok = m.idle[id]
	return
}

// QueryConsuming 查询一个Entry
func (m *Manager) QueryConsuming(id int) (v *Entry, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok = m.consuming[id]
	return
}

// QueryAbandon 查询一个Entry
func (m *Manager) QueryAbandon(id int) (v *Entry, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok = m.abandon[id]
	return
}

// NewEntry 创建新的Entry
func NewEntry(ans *mapper.Answer) *Entry {
	return &Entry{ID: ans.ID, State: Idle, Answer: ans}
}

// Idle 切换Entry状态为Idle, 需要先获取m的锁
func (e *Entry) Idle(m *Manager) {
	e.State = Idle
	delete(m.consuming, e.ID)
	m.idle[e.ID] = e
}

// Consuming 切换Entry状态为Consuming, 需要先获取m的锁
func (e *Entry) Consuming(m *Manager) {
	e.State = Consuming
	delete(m.idle, e.ID)
	m.consuming[e.ID] = e
}

// Finished 切换Entry状态为Finish并删除, 不需要获取锁
func (e *Entry) Finished(m *Manager) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e.State = Finished
	delete(m.consuming, e.ID)
}

// Abandon 切换Entry状态为Abandon, 需要先获取m的锁
func (e *Entry) Abandon(m *Manager) {
	e.State = Abandoned
	delete(m.consuming, e.ID)
	m.abandon[e.ID] = e
}

// Unabandon 切换Entry状态为Idle, 需要先获取m的锁
func (e *Entry) Unabandon(m *Manager) {
	e.State = Idle
	e.AbandonTimes = 0
	delete(m.abandon, e.ID)
	m.idle[e.ID] = e
}
