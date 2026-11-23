// Every SSA construct field.go propagates through, for TestTraceFieldGolden.
package main

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

type ID string

type Box struct{ ID ID }

type Holder interface{ Get() ID }

func (b Box) Get() ID { return b.ID }

var global ID

func store(id ID) { global = id }

func readGlobal() ID { return global }

func source(raw string) ID {
	s := strings.TrimSpace(raw)
	var id ID
	if s != "" {
		id = ID(s)
	} else {
		id = ID("x" + s)
	}
	return id
}

type Name string

func twice(id ID) [2]ID { return [2]ID{id, id} }

func sink(raw string) string {
	id := source(raw)
	store(id)
	ids := []ID{id}
	ids = append(ids, id)
	cp := make([]ID, len(ids))
	copy(cp, ids)
	m := map[string]ID{"k": cp[0]}
	m["j"] = id
	ch := make(chan ID, 1)
	ch <- m["k"]
	got := <-ch
	var h Holder = Box{ID: got}
	fn := func() ID { return h.Get() + ID(strconv.Itoa(len(ids))) }
	var last ID
	for _, v := range m {
		last = v
	}
	arr := [1]ID{fn()}
	p := &arr[0]
	u := url.URL{Path: string(*p)}
	_ = slices.ContainsFunc(ids, func(i ID) bool { return i == id })
	pre := id
	tag := func() string { return string(pre) }
	pair := twice(id)[1]
	ap := (*[2]ID)(ids)
	var w Name
	*(*ID)(&w) = ap[0]
	var st fmt.Stringer = &u
	var a any = st
	path := a.(*url.URL).Path
	lib := any(u).(url.URL).Path
	rs := strings.Map(func(r rune) rune { return r }, strings.Repeat(u.String(), 2))
	z, ok := m["z"]
	if !ok {
		z = last
	}
	k := any(z).(ID)
	n, _ := strconv.Atoi(string(ids[1:][0]))
	extra := tag() + string(pair) + string(w) + path + lib + string(k) + rs + st.String()
	final := string(h.(Box).ID) + u.Path + string(readGlobal()) + string(z) + strconv.Itoa(-n) + extra
	go fmt.Println(final)
	return final
}

func main() { fmt.Println(sink(" 42 ")) }
