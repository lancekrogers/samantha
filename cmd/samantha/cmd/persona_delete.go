package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/persona"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

func personaDeleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a persona profile and the prompt document it owns",
		Long: `Delete one persona profile.

Removes personas/<id>/ and prompts/persona/<id>.yaml. A prompt document another
persona points at is left in place and reported as kept.

Deleting the active persona re-activates the built-in samantha (or the first
remaining profile), so agent_name and the voice keys stay consistent.

--yes is required: this is not reversible.`,
		Args: cobra.ExactArgs(1),
		RunE: runPersonaDelete,
	}
	cmd.Flags().Bool("yes", false, "Confirm the deletion (required)")
	cmd.Flags().Bool("json", false, "Emit JSON")
	return cmd
}

func runPersonaDelete(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	asJSON, _ := cmd.Flags().GetBool("json")
	result, err := deletePersona(cmd, args[0])
	if err != nil {
		if asJSON {
			return emitJSONError(cmd, err)
		}
		return err
	}
	if asJSON {
		return encodeJSON(cmd, result)
	}
	printPersonaDelete(cmd, result)
	return nil
}

func deletePersona(cmd *cobra.Command, id string) (*personaDeleteJSON, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	// Existence first: asking someone to confirm deleting a persona that is not
	// there tells them the wrong thing about their install.
	if _, err := loadPersonaForCLI(id); err != nil {
		return nil, err
	}
	if yes, _ := cmd.Flags().GetBool("yes"); !yes {
		return nil, codedError(codeConfirmRequired, "deleting persona %q is not reversible: pass --yes to confirm", id)
	}
	res, err := persona.Delete(cfg, id)
	if err != nil {
		return nil, classifyDeleteError(id, err)
	}
	return &personaDeleteJSON{
		Deleted:       res.ID,
		Removed:       res.Removed,
		Kept:          res.Kept,
		ActivePersona: res.ActivePersona,
		Reactivated:   res.Reactivated,
	}, nil
}

// classifyDeleteError maps the persona package's refusals onto the codes a
// --json runner discriminates on.
func classifyDeleteError(id string, err error) error {
	switch {
	case errors.Is(err, persona.ErrBuiltinProtected):
		return codedError(codeBuiltinProtected, "%q is the built-in persona and cannot be deleted", id)
	case errors.Is(err, persona.ErrLastPersona):
		return codedError(codeLastPersona, "%q is the only persona: create another before deleting it", id)
	default:
		return err
	}
}

func printPersonaDelete(cmd *cobra.Command, r *personaDeleteJSON) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "\n  %s\n", titleStyle.Render("Deleted persona: "+r.Deleted))
	for _, path := range r.Removed {
		fmt.Fprintf(out, "  removed  %s\n", path)
	}
	for _, path := range r.Kept {
		fmt.Fprintf(out, "  kept     %s %s\n", path, dimStyle.Render("(shared prompt reference)"))
	}
	if r.Reactivated {
		fmt.Fprintf(out, "  active   %s %s\n", r.ActivePersona, dimStyle.Render("(re-activated)"))
	}
	fmt.Fprintln(out)
}
