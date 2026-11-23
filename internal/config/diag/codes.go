package diag

// Stable diagnostic codes for the lexer and parser (LOTUS-CFG-00xx).
// Codes are never reused or renumbered. Validator codes start at 0100.
const (
	CodeInvalidUTF8        = "LOTUS-CFG-0001" // file is not valid UTF-8
	CodeUnexpectedChar     = "LOTUS-CFG-0002" // character that can't start a token
	CodeUnterminatedString = "LOTUS-CFG-0003" // string without closing quote
	CodeInvalidEscape      = "LOTUS-CFG-0004" // bad escape sequence in a string
	CodeInvalidNumber      = "LOTUS-CFG-0005" // malformed number, size or duration
	CodeNumberRange        = "LOTUS-CFG-0006" // number does not fit in 64 bits
	CodeInvalidIdent       = "LOTUS-CFG-0007" // identifier with uppercase letters
	CodeFileTooLarge       = "LOTUS-CFG-0008" // file over the size limit
	CodeNestingTooDeep     = "LOTUS-CFG-0010" // blocks/lists nested too deeply
	CodeTooManyErrors      = "LOTUS-CFG-0011" // error limit reached
	CodeUnexpectedToken    = "LOTUS-CFG-0012" // token not allowed here
	CodeExpected           = "LOTUS-CFG-0013" // a specific token was required
	CodeReservedWord       = "LOTUS-CFG-0014" // reserved word used as a name
	CodeBadInterpolation   = "LOTUS-CFG-0015" // malformed ${name}
	CodeLetNotTopLevel     = "LOTUS-CFG-0016" // let inside a block
	CodeUnclosedBlock      = "LOTUS-CFG-0017" // '{' without matching '}'
	CodeUnclosedList       = "LOTUS-CFG-0018" // '[' or '(' without closer
)

// Loader codes: includes, variables, functions, version (LOTUS-CFG-01xx).
const (
	CodeIncludeNotFound    = "LOTUS-CFG-0101" // include path missing or unreadable
	CodeIncludeCycle       = "LOTUS-CFG-0102" // file includes itself, directly or not
	CodeIncludeDepth       = "LOTUS-CFG-0103" // includes nested too deeply
	CodeTooManyFiles       = "LOTUS-CFG-0104" // too many included files
	CodeInsecureInclude    = "LOTUS-CFG-0105" // included file or directory is not safe
	CodeBadIncludePath     = "LOTUS-CFG-0106" // include path or glob is not allowed
	CodeUndefinedVariable  = "LOTUS-CFG-0107" // $name was never defined
	CodeVariableRedefined  = "LOTUS-CFG-0108" // let for an existing name
	CodeEnvNotSet          = "LOTUS-CFG-0109" // env("NAME") with no default and NAME unset
	CodeBadFunction        = "LOTUS-CFG-0110" // unknown function or bad arguments
	CodeFileFunction       = "LOTUS-CFG-0111" // file() could not read the file
	CodeVariableType       = "LOTUS-CFG-0112" // variable used in a way its type doesn't allow
	CodeVersionMisplaced   = "LOTUS-CFG-0113" // lotus_config_version outside the root's first line
	CodeMissingVersion     = "LOTUS-CFG-0114" // root file doesn't start with lotus_config_version
	CodeUnsupportedVersion = "LOTUS-CFG-0115" // config language version not supported
)

// Validator codes: directives, arguments, contexts (LOTUS-CFG-012x..013x).
const (
	CodeUnknownDirective      = "LOTUS-CFG-0120" // no such directive
	CodeWrongContext          = "LOTUS-CFG-0121" // directive exists, but not here
	CodeDuplicateDirective    = "LOTUS-CFG-0122" // single directive given twice
	CodeDuplicateKey          = "LOTUS-CFG-0123" // keyed directive or map key repeated
	CodeArgCount              = "LOTUS-CFG-0124" // wrong number of arguments
	CodeInvalidValue          = "LOTUS-CFG-0125" // argument has the wrong type or value
	CodeBlockMismatch         = "LOTUS-CFG-0126" // block missing, or given where none is allowed
	CodePlaceholderNotAllowed = "LOTUS-CFG-0127" // {placeholder} in a directive that can't use it
	CodeUnknownPlaceholder    = "LOTUS-CFG-0128" // {name} is not a known placeholder
	CodeInvalidRegex          = "LOTUS-CFG-0129" // pattern doesn't compile (RE2)
	CodeInvalidOperator       = "LOTUS-CFG-0130" // operator not allowed here
	CodeTenantBoundary        = "LOTUS-CFG-0131" // included file defines a restricted directive
	CodeMissingRequired       = "LOTUS-CFG-0132" // required directive is absent
	CodeUnknownReference      = "LOTUS-CFG-0133" // name refers to nothing that is defined
	CodeInvalidCombination    = "LOTUS-CFG-0135" // settings that can't be used together
)

// Routing codes (LOTUS-CFG-03xx), see docs/architecture/routing-precedence.md.
const (
	CodeRouteDuplicate = "LOTUS-CFG-0301" // two routes with the same path and kind
	CodeRouteNesting   = "LOTUS-CFG-0302" // child route outside its parent prefix
	CodeRoutePath      = "LOTUS-CFG-0303" // route path is malformed
)

// Compiler codes: cross-checks that need the whole configuration (LOTUS-CFG-014x).
const (
	CodeSiteNoACME       = "LOTUS-CFG-0140" // tls automatic without an acme block
	CodeDuplicateHost    = "LOTUS-CFG-0141" // two sites claim the same hostname
	CodeDuplicateAddress = "LOTUS-CFG-0142" // two listeners bind the same address
	CodeNoListener       = "LOTUS-CFG-0143" // a site is attached to no listener
)

// CodeTLSOffOnHTTPS: a site with tls off is attached to an https listener.
const CodeTLSOffOnHTTPS = "LOTUS-CFG-0144"
