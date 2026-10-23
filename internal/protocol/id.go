package protocol

const MaxIDBytes = 64

func ValidID(id string) bool {
	if len(id) == 0 || len(id) > MaxIDBytes {
		return false
	}
	for index := 0; index < len(id); index++ {
		char := id[index]
		if char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			continue
		}
		if index > 0 && (char == '.' || char == '_' || char == '-') {
			continue
		}
		return false
	}
	return true
}
