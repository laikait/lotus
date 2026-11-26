package phpinstall

// RuntimeError means the Visual C++ runtime that PHP needs is not installed.
type RuntimeError struct{ Missing string }

func (e *RuntimeError) Error() string {
	return "PHP needs the Microsoft Visual C++ Redistributable (x64), and " + e.Missing + " is missing. Install it from " + RuntimeURL + " and try again."
}
