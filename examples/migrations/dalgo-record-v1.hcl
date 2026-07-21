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
      RecordWithID                   = "record.WithID"
      WithID                         = "record.WithKeyID"
      DataWithID                     = "record.DataWithID"
      Updates                        = "record.Updates"
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

  # RecordWithID used to be the embedded field name in DataWithID and in
  # application structs that embed it. Rename keyed struct-literal fields too.
  composite_field_rename "go" {
    from = "RecordWithID"
    to   = "WithID"
  }

  # Hierarchical Go campaigns add this requirement and a local worktree
  # replacement. It gives ordinary Go tooling a real module version too.
  go_module_require "github.com/dal-go/record" {
    version = "v0.1.0"
  }

  # These published modules replace campaign worktrees in publishable
  # consumer PRs.
  go_module_release "github.com/dal-go/record" {
    version = "v0.1.0"
  }

  go_module_release "github.com/dal-go/dalgo" {
    version = "v0.63.1"
  }

  go_module_release "github.com/strongo/strongoapp" {
    version = "v0.31.48"
  }

  go_module_release "github.com/bots-go-framework/bots-fw-telegram-models" {
    version = "v0.3.71"
  }

  go_module_release "github.com/dal-go/dalgo2firestore" {
    version = "v0.9.6"
  }

  go_module_release "github.com/sneat-co/commitius/backend" {
    version = "v0.2.3"
  }

  go_module_release "github.com/bots-go-framework/bots-fw-store-dalgo" {
    version = "v0.1.1"
  }

  go_module_release "github.com/sneat-co/sneat-go-core" {
    version = "v0.60.4"
  }

  go_module_release "github.com/bots-go-framework/bots-fw-telegram" {
    version = "v0.28.1"
  }

  go_module_release "github.com/sneat-co/gameboard/backend" {
    version = "v0.4.4"
  }

  go_module_release "github.com/sneat-co/ext-contactus/backend" {
    version = "v0.1.7"
  }

  go_module_release "github.com/sneat-co/sneat-core-modules" {
    version = "v0.53.6"
  }

  go_module_release "github.com/bots-go-framework/bots-fw-telegram-dalgo" {
    version = "v0.1.1"
  }

  go_module_release "github.com/sneat-co/assetus/backend" {
    version = "v0.3.7"
  }

  go_module_release "github.com/sneat-co/calendarius/backend" {
    version = "v0.4.7"
  }

  go_module_release "github.com/sneat-co/listus/backend" {
    version = "v0.1.12"
  }

  go_module_release "github.com/sneat-co/remindius/backend" {
    version = "v0.1.10"
  }

  go_module_release "github.com/sneat-co/sourcer/backend" {
    version = "v0.17.5"
  }

  go_module_release "github.com/sneat-co/togethered/backend" {
    version = "v0.6.1"
  }

  # DAL owns the executor as dal.ApplyChanges(ctx, tx, changes, ...), so the
  # following method invocation cannot safely be rewritten mechanically.
  review "changes-executor" {
    language        = "go"
    pattern         = "[.]ApplyChanges[(]"
    exclude_pattern = "dal[.]ApplyChanges[(]"
    message         = "Call dal.ApplyChanges with the transaction and record.Changes envelope."
  }

  # Go AST rewrites intentionally preserve comments and strings. Surface old
  # API spellings there so a resumed campaign cannot report a clean migration
  # while its documentation still teaches removed DALgo names.
  review "legacy-record-api" {
    language = "go"
    pattern  = "(github[.]com/dal-go/dalgo/(record|update)([^[:alnum:]_]|$)|dal[.](ErrNoError|ErrRecordNotFound|Key|KeyOption|FieldVal|Record|RecordWithID|WithID|DataWithID|Updates|EscapeID|EqualKeys|WithFields|WithParentKey|WithStringID|WithIntID|NewKeyWithID|NewKeyWithParentAndID|NewIncompleteKey|NewKeyWithFields|NewKeyWithOptions|NewWithID|NewDataWithID|NewRecord|NewRecordWithData|NewRecordWithIncompleteKey|NewRecordWithoutKey|AnyRecordWithError|DataToMap|MapToData|IsNotFound)([^[:alnum:]_]|$)|record[.](RecordWithID|WithRecordChanges)([^[:alnum:]_]|$)|WithRecordChanges([^[:alnum:]_]|$)|RecordWithID[[:space:]]*:)"
    message  = "Update comments, examples, or remaining code to the github.com/dal-go/record API."
  }
}
