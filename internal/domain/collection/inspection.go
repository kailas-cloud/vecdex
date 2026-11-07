package collection

// Inspection includes in-progress lifecycle states. Absent has no Collection.
type Inspection struct {
	Name       string
	Generation string
	State      string
	Collection *Collection
}
