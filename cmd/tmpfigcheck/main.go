package main

import (
	"fmt"

	"pii-guard/internal/config"
	"pii-guard/internal/engine"
	"pii-guard/internal/mask"
	"pii-guard/internal/pii"
)

func main() {
	reg := pii.NewRegistry()
	reg.Register(
		pii.NewNumericDetector(),
		pii.NewEmailDetector(),
		pii.NewFIODetector(),
		pii.NewDateDetector(config.DatePIIContext),
		pii.NewAddressDetector(),
		pii.NewBirthPlaceDetector(),
		pii.NewIssuerDetector(),
		pii.NewCitizenshipDetector(),
		pii.NewDriverLicenseLettersDetector(),
		pii.NewCardHolderDetector(),
		pii.NewExtraDocumentsDetector(),
		pii.NewExtraDetector(),
	)
	defs := config.Defaults{Preset: mask.Preset("full"), MinConfidence: 0.5, DateWithoutAnchor: config.DatePIIContext}
	sys := config.System{Name: "alfasonar", Enabled: true, AllTypes: true, Demask: true, Preset: mask.Preset("full"),
		Exclusions: config.Exclusions{PublicFigures: true, OrgAddresses: true}}
	e := engine.New(reg)
	texts := []string{
		"Петрова Сергея Ивановича нам неизвестна.",
		"Биография Петрова Сергея Ивановича нам неизвестна.",
		"Сведений о Петрове Сергее Ивановиче нет.",
		"Кто такой Петров Сергей Иванович?",
		"Иванов Иван Иванович подал заявление.",
		"Глава 5. Порядок расчётов. Ответственный Иванов Иван Иванович.",
		"Порядок расчётов. Ответственный Иванов Иван Иванович.",
		"Кредитная политика утверждена. Составил Смирнов Алексей.",
		"Условия утверждены. Составил Смирнов Алексей.",
		"Мэрия ответила. Обращался Фёдоров Игорь.",
		"Ответ получен. Обращался Фёдоров Игорь.",
		"Блогер написал отзыв, его зовут Волков Тимур Петрович.",
		"Человек написал отзыв, его зовут Волков Тимур Петрович.",
	}
	for _, t := range texts {
		r := e.Mask(t, sys, defs)
		flag := "  "
		if r.Text == t {
			flag = "!!"
		}
		fmt.Printf("%s %-64s -> %s\n", flag, t, r.Text)
	}
}
