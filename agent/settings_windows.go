//go:build windows

package main

import (
	"log"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
)

func showSettings() {
	cfg := loadConfig()

	var dlg *walk.Dialog
	var relayURLEdit *walk.LineEdit
	var tokenEdit *walk.LineEdit
	var profileCombo *walk.ComboBox
	var autoStartCB *walk.CheckBox

	profileOptions := []string{"1080", "720"}
	profileIndex := 0
	if cfg.Profile == 720 {
		profileIndex = 1
	}

	_, err := Dialog{
		AssignTo: &dlg,
		Title:    "OpsView Agent 설정",
		MinSize:  Size{Width: 400, Height: 280},
		Layout:   VBox{},
		Children: []Widget{
			Composite{
				Layout: Grid{Columns: 2},
				Children: []Widget{
					Label{Text: "Relay URL:"},
					LineEdit{AssignTo: &relayURLEdit, Text: cfg.RelayURL},

					Label{Text: "Token:"},
					LineEdit{AssignTo: &tokenEdit, Text: cfg.Token, PasswordMode: true},

					Label{Text: "Profile:"},
					ComboBox{
						AssignTo:     &profileCombo,
						Model:        profileOptions,
						CurrentIndex: profileIndex,
					},
				},
			},
			CheckBox{
				AssignTo: &autoStartCB,
				Text:     "Windows 시작 시 자동 실행",
				Checked:  cfg.AutoStart,
			},
			Composite{
				Layout: HBox{},
				Children: []Widget{
					HSpacer{},
					PushButton{
						Text: "저장",
						OnClicked: func() {
							cfg.RelayURL = relayURLEdit.Text()
							cfg.Token = tokenEdit.Text()
							if profileCombo.CurrentIndex() == 1 {
								cfg.Profile = 720
							} else {
								cfg.Profile = 1080
							}
							newAutoStart := autoStartCB.Checked()
							if newAutoStart != cfg.AutoStart {
								setAutoStart(newAutoStart)
								cfg.AutoStart = newAutoStart
								syncTrayAutoStart(newAutoStart)
							}
							if err := saveConfig(cfg); err != nil {
								walk.MsgBox(dlg, "Error", "설정 저장 실패: "+err.Error(), walk.MsgBoxIconError)
								return
							}
							log.Printf("[settings] saved: relay=%s profile=%d autostart=%v", cfg.RelayURL, cfg.Profile, cfg.AutoStart)
							dlg.Accept()
							go restartAgentIfRunning()
						},
					},
					PushButton{
						Text:      "취소",
						OnClicked: func() { dlg.Cancel() },
					},
				},
			},
		},
	}.Run(nil)

	if err != nil {
		log.Printf("[settings] dialog error: %v", err)
	}
}
