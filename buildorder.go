package main

import (
	"container/heap"
)

func createOrder(packages map[string]*Package) (order []*Package, err error) {
	order = make([]*Package, len(packages))

	fullGraph := make(map[string]*PackageNode, len(packages))
	for k := range packages {
		build(k, packages, fullGraph)
	}

	pq := make(PriorityQueue, len(packages))
	i := 0
	for v := range fullGraph {
		pq[i] = &Item{
			value:    fullGraph[v],
			priority: -fullGraph[v].Priority, // Invert priority queue sorting
			index:    i,
		}
		i++
	}

	heap.Init(&pq)

	for i := 0; i < len(order); i++ {
		item := heap.Pop(&pq).(*Item)
		order[i] = packages[item.value.Name]
	}

	return order, nil
}

func build(name string, packages map[string]*Package, graph map[string]*PackageNode) int {
	if _, ok := graph[name]; ok {
		return graph[name].Priority
	}

	if len(packages[name].Depends) == 0 {
		graph[name] = &PackageNode{Name: name, Priority: 1}
		return 1
	}

	newNode := &PackageNode{Name: name, Priority: 2}

	graph[newNode.Name] = newNode
	highest := -1
	for _, s := range packages[name].Depends {
		k := build(s, packages, graph)
		if k > highest {
			highest = k
		}
	}
	newNode.Priority += highest

	return newNode.Priority
}

type PackageNode struct {
	Name     string
	Priority int
	Children []*PackageNode
}

// An Item is something we manage in a priority queue.
type Item struct {
	value    *PackageNode // The value of the item; arbitrary.
	priority int          // The priority of the item in the queue.
	// The index is needed by update and is maintained by the heap.Interface methods.
	index int // The index of the item in the heap.
}

// A PriorityQueue implements heap.Interface and holds Items.
type PriorityQueue []*Item

func (pq PriorityQueue) Len() int { return len(pq) }

func (pq PriorityQueue) Less(i, j int) bool {
	// We want Pop to give us the highest, not lowest, priority so we use greater than here.
	return pq[i].priority > pq[j].priority
}

func (pq PriorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}

func (pq *PriorityQueue) Push(x interface{}) {
	n := len(*pq)
	item := x.(*Item)
	item.index = n
	*pq = append(*pq, item)
}

func (pq *PriorityQueue) Pop() interface{} {
	old := *pq
	n := len(old)
	item := old[n-1]
	old[n-1] = nil  // avoid memory leak
	item.index = -1 // for safety
	*pq = old[0 : n-1]
	return item
}

// update modifies the priority and value of an Item in the queue.
func (pq *PriorityQueue) update(item *Item, value *PackageNode, priority int) {
	item.value = value
	item.priority = priority
	heap.Fix(pq, item.index)
}
