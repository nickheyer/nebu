// Package precision puts a weight group's bits per weight into words.
package precision

// The words for one bit width and every width down to the next
type Level struct {
	Bits  uint32
	Label string
	Blurb string
	// Quality retained, 0 unknown through 5 full
	Quality uint32
}

// Words for weights by how many bits each takes
type Scale interface {
	// The level a width falls in, the first level at or below it
	Level(bits uint32) Level
	Levels() []Level
}
