package outbox

// SetAfterPublish — хук тестов: после подтверждения JetStream, до пометки published_at.
func (p *Publisher) SetAfterPublish(f func(published int) error) { p.afterPublish = f }
