package bgp

import "net"

type TrieNode struct {
	children   [2]*TrieNode
	routes     []Route
	isTerminal bool
}

type Trie struct {
	root4 TrieNode
	root6 TrieNode
}

func (t *Trie) Insert(prefix *net.IPNet, route Route) {
	ones, _ := prefix.Mask.Size()
	root, ipBytes := t.rootFor(prefix.IP)

	node := root
	for i := 0; i < ones; i++ {
		bit := (ipBytes[i/8] >> (7 - uint(i%8))) & 1
		if node.children[bit] == nil {
			node.children[bit] = &TrieNode{}
		}
		node = node.children[bit]
	}
	node.isTerminal = true
	node.routes = append(node.routes, route)
}

func (t *Trie) LookupIP(ip net.IP) []Route {
	root, ipBytes, maxBits := t.rootForIP(ip)
	node := root
	var best []Route

	for i := 0; i < maxBits && node != nil; i++ {
		if node.isTerminal {
			best = node.routes
		}
		bit := (ipBytes[i/8] >> (7 - uint(i%8))) & 1
		node = node.children[bit]
	}
	if node != nil && node.isTerminal {
		best = node.routes
	}
	return best
}

func (t *Trie) LookupExactPrefix(prefix *net.IPNet) []Route {
	ones, _ := prefix.Mask.Size()
	root, ipBytes := t.rootFor(prefix.IP)

	node := root
	for i := 0; i < ones; i++ {
		bit := (ipBytes[i/8] >> (7 - uint(i%8))) & 1
		if node.children[bit] == nil {
			return nil
		}
		node = node.children[bit]
	}
	return node.routes
}

func (t *Trie) rootFor(ip net.IP) (*TrieNode, []byte) {
	if v4 := ip.To4(); v4 != nil {
		return &t.root4, v4
	}
	return &t.root6, ip.To16()
}

func (t *Trie) rootForIP(ip net.IP) (root *TrieNode, b []byte, bits int) {
	if v4 := ip.To4(); v4 != nil {
		return &t.root4, v4, 32
	}
	return &t.root6, ip.To16(), 128
}
