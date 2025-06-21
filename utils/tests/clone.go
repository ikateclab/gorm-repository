package tests

// Clone creates a deep copy of the TestDBConfig struct
func (original *TestDBConfig) Clone() *TestDBConfig {
	if original == nil {
		return nil
	}
	// Create new instance - all fields are simple types
	clone := *original
	return &clone
}

// Clone creates a deep copy of the TestUserBuilder struct
func (original *TestUserBuilder) Clone() *TestUserBuilder {
	if original == nil {
		return nil
	}
	// Create new instance and copy all simple fields
	clone := *original

	// Only handle JSONB fields that need deep cloning

	if original.user != nil {
		clone.user = original.user.Clone()
	}

	return &clone
}

// Clone creates a deep copy of the TestProfileBuilder struct
func (original *TestProfileBuilder) Clone() *TestProfileBuilder {
	if original == nil {
		return nil
	}
	// Create new instance and copy all simple fields
	clone := *original

	// Only handle JSONB fields that need deep cloning

	if original.profile != nil {
		clone.profile = original.profile.Clone()
	}

	return &clone
}

// Clone creates a deep copy of the TestPostBuilder struct
func (original *TestPostBuilder) Clone() *TestPostBuilder {
	if original == nil {
		return nil
	}
	// Create new instance and copy all simple fields
	clone := *original

	// Only handle JSONB fields that need deep cloning

	if original.post != nil {
		clone.post = original.post.Clone()
	}

	return &clone
}

// Clone creates a deep copy of the TestUser struct
func (original *TestUser) Clone() *TestUser {
	if original == nil {
		return nil
	}
	// Create new instance - all fields are simple types
	clone := *original
	return &clone
}

// Clone creates a deep copy of the TestProfile struct
func (original *TestProfile) Clone() *TestProfile {
	if original == nil {
		return nil
	}
	// Create new instance - all fields are simple types
	clone := *original
	return &clone
}

// Clone creates a deep copy of the TestPost struct
func (original *TestPost) Clone() *TestPost {
	if original == nil {
		return nil
	}
	// Create new instance - all fields are simple types
	clone := *original
	return &clone
}

// Clone creates a deep copy of the TestTag struct
func (original *TestTag) Clone() *TestTag {
	if original == nil {
		return nil
	}
	// Create new instance - all fields are simple types
	clone := *original
	return &clone
}

// Clone creates a deep copy of the TestSimpleEntity struct
func (original *TestSimpleEntity) Clone() *TestSimpleEntity {
	if original == nil {
		return nil
	}
	// Create new instance - all fields are simple types
	clone := *original
	return &clone
}
