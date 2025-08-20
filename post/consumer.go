package post

type (
	Consumer struct {
		Manager *Manager
	}
)

func NewConsumer(m *Manager) *Consumer {
	return &Consumer{Manager: m}
}

// Consume 开始消费
func (c *Consumer) Consume() {

}
