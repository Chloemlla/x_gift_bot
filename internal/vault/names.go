package vault

// NamesWithPrefix lists record names without decrypting unrelated payloads.
func (v *Vault) NamesWithPrefix(prefix string) ([]string, error) {
	rows, err := v.db.Query("SELECT name FROM secrets WHERE name >= ? AND name < ? ORDER BY name", prefix, prefix+"\xff")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}
