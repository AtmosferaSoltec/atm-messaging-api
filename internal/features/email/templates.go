package email

import (
	"bytes"
	"embed"
	"fmt"
	htmltemplate "html/template"
	texttemplate "text/template"
)

// The .html files are compiled email HTML (from MJML) with Go template fields.
// html/template escapes every value.
//
//go:embed templates/*.html
var templateFiles embed.FS

var htmlTemplates = htmltemplate.Must(htmltemplate.ParseFS(templateFiles, "templates/*.html"))

var textTemplates = texttemplate.Must(texttemplate.New(TemplatePasswordResetCode).Parse(
	`Recupera tu contraseña

{{if .Name}}Hola {{.Name}}. {{end}}Escribe este código en la app para crear una contraseña nueva:

    {{.Code}}

Vence en {{.Minutes}} minutos y solo sirve una vez.

Si no lo pediste, ignora este correo: tu contraseña no cambia.
`))

func init() {
	texttemplate.Must(textTemplates.New(TemplatePasswordChanged).Parse(
		`Tu contraseña se cambió

{{if .Name}}Hola {{.Name}}. {{end}}La contraseña de tu cuenta se acaba de cambiar y se cerraron las sesiones abiertas en tus dispositivos.

Si no fuiste tú, recupera tu contraseña desde la app ahora mismo.
`))
}

// view is what the templates see.
type view struct {
	Name    string
	Code    string
	Minutes int
}

// Rendered is a message ready to hand to a Sender (To is filled by the caller).
type Rendered struct {
	Subject string
	HTML    string
	Text    string
}

// Render builds the subject, HTML and plain-text bodies of a template.
func Render(template string, data Data) (Rendered, error) {
	v := view{Name: data.Name, Code: data.Code}
	if data.ExpiresMinutes != nil {
		v.Minutes = *data.ExpiresMinutes
	}
	var subject string
	switch template {
	case TemplatePasswordResetCode:
		subject = "Tu código de verificación: " + data.Code
	case TemplatePasswordChanged:
		subject = "Tu contraseña se cambió"
	default:
		return Rendered{}, fmt.Errorf("render: unknown template %q", template)
	}
	var html, text bytes.Buffer
	if err := htmlTemplates.ExecuteTemplate(&html, template+".html", v); err != nil {
		return Rendered{}, fmt.Errorf("render %s html: %w", template, err)
	}
	if err := textTemplates.ExecuteTemplate(&text, template, v); err != nil {
		return Rendered{}, fmt.Errorf("render %s text: %w", template, err)
	}
	return Rendered{Subject: subject, HTML: html.String(), Text: text.String()}, nil
}
