/*
Copyright 2019-2020 vChain, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package tbtree

import (
	"encoding/binary"
	"errors"
	"io"
)

var ErrReadersNotClosed = errors.New("readers not closed")

const (
	InnerNodeType = iota
	RootInnerNodeType
	LeafNodeType
	RootLeafNodeType
)

type Snapshot struct {
	t           *TBtree
	id          uint64
	root        node
	readers     map[int]*Reader
	maxReaderID int
	closed      bool
}

func (s *Snapshot) Get(key []byte) (value []byte, ts uint64, err error) {
	if s.closed {
		return nil, 0, ErrAlreadyClosed
	}

	return s.root.get(key)
}

func (s *Snapshot) GetTs(key []byte, limit int64) (ts []uint64, err error) {
	if s.closed {
		return nil, ErrAlreadyClosed
	}

	if limit < 1 {
		return nil, ErrIllegalArgument
	}

	return s.root.getTs(key, limit)
}

func (s *Snapshot) Ts() uint64 {
	return s.root.ts()
}

func (s *Snapshot) Reader(spec *ReaderSpec) (*Reader, error) {
	if s.closed {
		return nil, ErrAlreadyClosed
	}

	if spec == nil {
		return nil, ErrIllegalArgument
	}

	path, startingLeaf, startingOffset, err := s.root.findLeafNode(spec.initialKey, nil, nil, spec.ascOrder)
	if err == ErrKeyNotFound {
		return nil, ErrNoMoreEntries
	}
	if err != nil {
		return nil, err
	}

	reader := &Reader{
		snapshot:   s,
		id:         s.maxReaderID,
		initialKey: spec.initialKey,
		isPrefix:   spec.isPrefix,
		ascOrder:   spec.ascOrder,
		path:       path,
		leafNode:   startingLeaf,
		offset:     startingOffset,
		closed:     false,
	}

	s.readers[reader.id] = reader

	s.maxReaderID++

	return reader, nil
}

func (s *Snapshot) closedReader(r *Reader) error {
	if s.closed {
		return ErrAlreadyClosed
	}

	delete(s.readers, r.id)

	return nil
}

func (s *Snapshot) Close() error {
	if s.closed {
		return ErrAlreadyClosed
	}

	if len(s.readers) > 0 {
		return ErrReadersNotClosed
	}

	err := s.t.snapshotClosed(s)
	if err != nil {
		return err
	}

	s.closed = true

	return nil
}

func (s *Snapshot) WriteTo(w io.Writer, writeOpts *WriteOpts) (int64, error) {
	_, n, err := s.root.writeTo(w, true, writeOpts)
	return n, err
}

func (n *innerNode) writeTo(w io.Writer, asRoot bool, writeOpts *WriteOpts) (off int64, tw int64, err error) {
	if writeOpts.OnlyMutated && !n.mutated() {
		return n.off, 0, nil
	}

	var cw int64

	offsets := make([]int64, len(n.nodes))

	for i, c := range n.nodes {
		wopts := &WriteOpts{
			OnlyMutated: writeOpts.OnlyMutated,
			BaseOffset:  writeOpts.BaseOffset + cw,
			commitLog:   writeOpts.commitLog,
		}

		o, w, err := c.writeTo(w, false, wopts)
		if err != nil {
			return 0, w, err
		}

		offsets[i] = o
		cw += w
	}

	size := n.size()

	buf := make([]byte, size+4)
	bi := 0

	if asRoot {
		buf[bi] = RootInnerNodeType
	} else {
		buf[bi] = InnerNodeType
	}
	bi++

	binary.BigEndian.PutUint32(buf[bi:], uint32(size)) // Size
	bi += 4

	binary.BigEndian.PutUint32(buf[bi:], uint32(len(n.nodes)))
	bi += 4

	for i, c := range n.nodes {
		n := writeNodeRefToWithOffset(c, offsets[i], buf[bi:])
		bi += n
	}

	if asRoot {
		binary.BigEndian.PutUint32(buf[bi:], uint32(size)) // Size
		bi += 4
	}

	wn, err := w.Write(buf[:bi])
	if err != nil {
		return 0, int64(wn), err
	}

	if writeOpts.commitLog {
		n.off = writeOpts.BaseOffset + cw
		n.mut = false
		n.t.cachePut(n)
	}

	tw = cw + int64(size)

	if asRoot {
		tw += 4
	}

	return writeOpts.BaseOffset + cw, tw, nil
}

func (l *leafNode) writeTo(w io.Writer, asRoot bool, writeOpts *WriteOpts) (off int64, tw int64, err error) {
	if writeOpts.OnlyMutated && !l.mutated() {
		return l.off, 0, nil
	}

	size := l.size()
	buf := make([]byte, size+4)
	bi := 0

	if asRoot {
		buf[bi] = RootLeafNodeType
	} else {
		buf[bi] = LeafNodeType
	}
	bi++

	binary.BigEndian.PutUint32(buf[bi:], uint32(size)) // Size
	bi += 4

	var cw int64
	var prevNodeOff int64

	if l.prevNode != nil {
		if l.prevNode.mutated() {
			o, w, err := l.prevNode.writeTo(w, false, writeOpts)
			if err != nil {
				return 0, w, err
			}
			prevNodeOff = o
			cw = w
		} else {
			prevNodeOff = l.prevNode.offset()
		}
	}

	binary.BigEndian.PutUint64(buf[bi:], uint64(prevNodeOff))
	bi += 8

	binary.BigEndian.PutUint32(buf[bi:], uint32(len(l.values)))
	bi += 4

	for _, v := range l.values {
		binary.BigEndian.PutUint32(buf[bi:], uint32(len(v.key)))
		bi += 4

		copy(buf[bi:], v.key)
		bi += len(v.key)

		binary.BigEndian.PutUint32(buf[bi:], uint32(len(v.value)))
		bi += 4

		copy(buf[bi:], v.value)
		bi += len(v.value)

		binary.BigEndian.PutUint32(buf[bi:], uint32(v.tsLen))
		bi += 4

		for i := 0; i < v.tsLen; i++ {
			binary.BigEndian.PutUint64(buf[bi:], v.ts[i])
			bi += 8
		}
	}

	if asRoot {
		binary.BigEndian.PutUint32(buf[bi:], uint32(size)) // Size
		bi += 4
	}

	n, err := w.Write(buf[:bi])
	if err != nil {
		return 0, int64(n), err
	}

	if writeOpts.commitLog {
		l.off = writeOpts.BaseOffset + cw
		l.mut = false
		l.t.cachePut(l)
	}

	tw = cw + int64(size)

	if asRoot {
		tw += 4
	}

	return writeOpts.BaseOffset + cw, tw, nil
}

func (n *nodeRef) writeTo(w io.Writer, asRoot bool, writeOpts *WriteOpts) (int64, int64, error) {
	if writeOpts.OnlyMutated {
		return n.off, 0, nil
	}

	node, err := n.t.nodeAt(n.off)
	if err != nil {
		return 0, 0, err
	}

	off, tw, err := node.writeTo(w, asRoot, writeOpts)
	if err != nil {
		return 0, tw, err
	}

	if writeOpts.commitLog {
		n.off = off
	}

	return off, tw, nil
}

func writeNodeRefToWithOffset(n node, offset int64, buf []byte) int {
	i := 0

	maxKey := n.maxKey()
	binary.BigEndian.PutUint32(buf[i:], uint32(len(maxKey)))
	i += 4

	copy(buf[i:], maxKey)
	i += len(maxKey)

	binary.BigEndian.PutUint64(buf[i:], n.ts())
	i += 8

	binary.BigEndian.PutUint32(buf[i:], uint32(n.size()))
	i += 4

	binary.BigEndian.PutUint64(buf[i:], uint64(offset))
	i += 8

	return i
}