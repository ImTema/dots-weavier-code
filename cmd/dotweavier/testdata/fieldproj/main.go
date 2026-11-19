package main

import (
	"fmt"
	"strconv"
)

type ID int

type Request struct{ ID *ID }

type Loan struct{ ID ID }

type Result struct{ ID ID }

func convert(raw string) (*Request, error) {
	req := &Request{}
	if raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return nil, err
		}
		id := ID(n)
		req.ID = &id
	}
	return req, nil
}

func adapt(r *Request) Loan {
	return Loan{ID: *r.ID}
}

func process(l Loan) *Result {
	res := &Result{}
	res.ID = keep(l.ID)
	return res
}

func keep(id ID) ID { return id }

func unrelated() ID { return keep(7) }

func main() {
	req, _ := convert("42")
	if req.ID == nil {
		fresh := ID(1)
		req.ID = &fresh
	}
	fmt.Println(process(adapt(req)).ID, unrelated())
}
