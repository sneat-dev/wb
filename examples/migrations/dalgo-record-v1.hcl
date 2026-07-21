format = "https://sneat.dev/workbench/formats/migration/v1"

migration "dalgo-record-v1" {
  title = "Extract DALgo's persistence-neutral record model"

  scope {
    languages = ["go"]
  }

  import_replace "go" {
    from = "github.com/dal-go/dalgo/record"
    to   = "github.com/dal-go/record"
  }

  import_replace "go" {
    from = "github.com/dal-go/dalgo/update"
    to   = "github.com/dal-go/record/update"
  }

  selector_rewrite "go" {
    import        = "github.com/dal-go/dalgo/dal"
    add_import    = "github.com/dal-go/record"
    add_import_as = "record"

    rewrites = {
      ErrNoError                     = "record.ErrNoError"
      ErrRecordNotFound               = "record.ErrRecordNotFound"
      Key                            = "record.Key"
      KeyOption                      = "record.KeyOption"
      FieldVal                       = "record.FieldVal"
      Record                         = "record.Record"
      WithID                         = "record.WithKeyID"
      DataWithID                     = "record.DataWithID"
      Updates                        = "record.Updates"
      Changes                        = "record.Changes"
      EscapeID                       = "record.EscapeID"
      EqualKeys                      = "record.EqualKeys"
      WithFields                     = "record.WithFields"
      WithParentKey                  = "record.WithParentKey"
      WithStringID                   = "record.WithStringID"
      WithIntID                      = "record.WithIntID"
      NewKeyWithID                   = "record.NewKeyWithID"
      NewKeyWithParentAndID          = "record.NewKeyWithParentAndID"
      NewIncompleteKey               = "record.NewIncompleteKey"
      NewKeyWithFields               = "record.NewKeyWithFields"
      NewKeyWithOptions              = "record.NewKeyWithOptions"
      NewWithID                      = "record.NewWithID"
      NewDataWithID                  = "record.NewDataWithID"
      NewRecord                      = "record.NewRecord"
      NewRecordWithData              = "record.NewRecordWithData"
      NewRecordWithIncompleteKey     = "record.NewRecordWithIncompleteKey"
      NewRecordWithoutKey            = "record.NewRecordWithoutKey"
      AnyRecordWithError             = "record.AnyRecordWithError"
      DataToMap                      = "record.DataToMap"
      MapToData                      = "record.MapToData"
      IsNotFound                     = "record.IsNotFound"
    }
  }

  # This is intentionally a separate, repeatable block type. More than one
  # selector_rename "go" block is valid in a migration document.
  selector_rename "go" {
    import = "github.com/dal-go/record"
    from   = "WithRecordChanges"
    to     = "Changes"
  }

  # Hierarchical Go campaigns add this requirement and a local worktree
  # replacement. It gives ordinary Go tooling a real module version too.
  go_module_require "github.com/dal-go/record" {
    version = "v0.1.0"
  }

  # DAL owns the executor as dal.ApplyChanges(ctx, tx, changes, ...), so the
  # following method invocation cannot safely be rewritten mechanically.
  review "changes-executor" {
    language = "go"
    pattern  = "[.]ApplyChanges[(]"
    message  = "Call dal.ApplyChanges with the transaction and record.Changes envelope."
  }
}
