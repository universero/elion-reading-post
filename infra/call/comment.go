package call

type CommentTask struct {
}

func NewCommentTask() *CommentTask {
	return &CommentTask{}
}

func (t *CommentTask) Submit() (bool, error) {
	return false, nil
}

func (t *CommentTask) Query() (string, error) {
	return "", nil
}
