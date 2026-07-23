package httpapi

type BodyOutput[T any] struct {
	Body T
}

type CreatedOutput[T any] struct {
	Status int `status:"201"`
	Body   T
}

type AcceptedBodyOutput[T any] struct {
	Status int `status:"202"`
	Body   T
}

type OKStatusOutput struct {
	Status int `status:"200"`
}

type AcceptedStatusOutput struct {
	Status int `status:"202"`
}
